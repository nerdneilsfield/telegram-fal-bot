package bot

import (
	"context"
	"fmt"
	"runtime/debug" // 导入 debug 包
	"time"

	auth "github.com/nerdneilsfield/telegram-fal-bot/internal/auth"
	cfg "github.com/nerdneilsfield/telegram-fal-bot/internal/config" // Optional
	loggerPkg "github.com/nerdneilsfield/telegram-fal-bot/internal/logger"
	"github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
	fapi "github.com/nerdneilsfield/telegram-fal-bot/pkg/falapi"

	"os"
	"os/signal"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

func StartBot(config *cfg.Config, version string, buildDate string) {
	// Initialize logger as local variable
	logger, err := loggerPkg.InitLogger(config.LogConfig.Level, config.LogConfig.Format, config.LogConfig.File)
	if err != nil {
		// Fallback or handle error if logger isn't global
		panic(fmt.Sprintf("Logger not initialized: %v", err))
	}

	bot, err := tgbotapi.NewBotAPIWithAPIEndpoint(config.BotToken, config.TelegramAPIURL)
	if err != nil {
		logger.Fatal("Failed to create bot API", zap.Error(err))
	}
	bot.Debug = false // Set to true for verbose API logging
	logger.Info("Authorized on account", zap.String("username", bot.Self.UserName))

	// 添加设置命令的代码
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "开始使用 Bot"},
		{Command: "balance", Description: "查询余额"},
		{Command: "loras", Description: "查看可用风格"},
		{Command: "version", Description: "查看版本信息"},
		// 你可以在这里添加更多命令，例如 /help
	}
	commandsConfig := tgbotapi.NewSetMyCommands(commands...)
	if _, err := bot.Request(commandsConfig); err != nil {
		logger.Error("Failed to set bot commands", zap.Error(err))
		// 通常不需要因为设置命令失败而停止 Bot，记录错误即可
	} else {
		logger.Info("Successfully set bot commands")
	}
	// 结束设置命令的代码

	// 初始化依赖
	falClient := fapi.NewClient(config)
	authorizer := auth.NewAuthorizer(config.Auth.AuthorizedUserIDs, config.Admins.AdminUserIDs)
	stateManager := NewStateManager()
	var balanceManager *storage.GormBalanceManager
	if config.Balance.CostPerGeneration > 0 { // 仅当配置了成本时启用余额管理
		db, err := storage.InitDB(config.DBPath)
		if err != nil {
			logger.Fatal("Failed to create database or open database", zap.Error(err))
		}
		balanceManager = storage.NewGormBalanceManager(db, config.Balance.InitialBalance, config.Balance.CostPerGeneration)
		logger.Info("Balance management enabled", zap.Float64("initial", config.Balance.InitialBalance), zap.Float64("cost", config.Balance.CostPerGeneration))
	}

	// 初始化 lora 配置
	baseLoraList := []LoraConfig{}
	for _, lora := range config.BaseLoRAs {
		loraConfig, err := GenerateLoraConfig(lora)
		if err != nil {
			logger.Fatal("Failed to generate lora config", zap.Error(err))
		}
		baseLoraList = append(baseLoraList, loraConfig)
	}

	// 初始化 lora 配置
	loras := []LoraConfig{}
	for _, lora := range config.LoRAs {
		loraConfig, err := GenerateLoraConfig(lora)
		if err != nil {
			logger.Fatal("Failed to generate lora config", zap.Error(err))
		}
		loras = append(loras, loraConfig)
	}

	deps := BotDeps{
		Bot:            bot,
		FalClient:      falClient,
		Config:         config,
		StateManager:   stateManager,
		BalanceManager: balanceManager,
		Authorizer:     authorizer,
		BaseLoRA:       baseLoraList,
		LoRA:           loras,
		Version:        version,
		BuildDate:      buildDate,
		Logger:         logger, // Assign initialized logger to deps
	}

	u := tgbotapi.NewUpdate(0) // 0 means no offset, get all pending updates
	u.Timeout = 120

	// 使用 context 控制 goroutine 生命周期
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates := bot.GetUpdatesChan(u)

	// 优雅地处理中断信号
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs // 等待中断信号
		logger.Info("Received interrupt signal, shutting down...")
		cancel() // 通知 update loop 停止
		// 可以添加额外的清理逻辑
	}()

	logger.Info("Starting update processing loop...")
	for {
		select {
		case update := <-updates:
			// 权限检查
			var userID int64
			if update.Message != nil {
				userID = update.Message.From.ID
			} else if update.CallbackQuery != nil {
				userID = update.CallbackQuery.From.ID
			} else {
				continue // 忽略其他类型的更新
			}

			if !authorizer.IsAuthorized(userID) {
				logger.Warn("Unauthorized access attempt", zap.Int64("user_id", userID))
				if update.Message != nil {
					bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "抱歉，您无权使用此机器人。"))
				} else if update.CallbackQuery != nil {
					// 对 CallbackQuery 的未授权用户不直接回复消息，仅记录
					// bot.AnswerCallbackQuery(tgbotapi.NewCallback(update.CallbackQuery.ID, "无权操作"))
					// 使用 Request 方法替代
					callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "无权操作")
					if _, err := bot.Request(callback); err != nil {
						logger.Error("Failed to answer callback query for unauthorized user", zap.Error(err), zap.Int64("user_id", userID))
					}
				}
				continue
			}

			// 异步处理每个用户的更新，避免阻塞主循环
			go func(u tgbotapi.Update) {
				// 可以添加 panic recovery
				defer func() {
					if r := recover(); r != nil {
						errMsg := fmt.Sprintf("%v", r)
						stackTrace := string(debug.Stack())
						logger.Error("Panic recovered in handler", zap.Any("error", errMsg), zap.String("stack", stackTrace)) // Log full stack trace

						// 尝试获取 chatID 和 userID
						var chatID int64
						var userID int64
						if u.Message != nil {
							chatID = u.Message.Chat.ID
							userID = u.Message.From.ID
						} else if u.CallbackQuery != nil { // Need to check CallbackQuery.From for UserID
							userID = u.CallbackQuery.From.ID
							if u.CallbackQuery.Message != nil { // Message might be nil in some callback scenarios?
								chatID = u.CallbackQuery.Message.Chat.ID
							} else {
								// If message is nil in callback, cannot easily determine chatID to reply.
								logger.Warn("Panic recovery: CallbackQuery.Message is nil, cannot determine chatID to reply", zap.Int64("user_id", userID))
								// Optional: Try sending to userID directly if it's expected to be a private chat?
								// chatID = userID
							}
						}

						if chatID != 0 { // Only proceed if we have a chatID to send to
							// 检查是否为管理员
							isAdmin := false
							for _, adminID := range deps.Config.Admins.AdminUserIDs {
								if userID == adminID {
									isAdmin = true
									break
								}
							}

							if isAdmin {
								// Send detailed error to admin
								detailedMsg := fmt.Sprintf("☢️ Panic Recovered ☢️\nError: %s\n\nTraceback:\n```\n%s\n```", errMsg, stackTrace)
								const maxMsgLen = 4090 // Keep some buffer below 4096
								if len(detailedMsg) > maxMsgLen {
									detailedMsg = detailedMsg[:maxMsgLen] + "\n...(truncated)```" // Ensure markdown block is closed
								}
								// Use Markdown parse mode for the code block
								msg := tgbotapi.NewMessage(chatID, detailedMsg)
								msg.ParseMode = tgbotapi.ModeMarkdown
								if _, errSend := bot.Send(msg); errSend != nil {
									logger.Error("Failed to send panic details to admin", zap.Error(errSend), zap.Int64("admin_id", userID))
								}
							} else {
								// Send generic error to non-admin user
								bot.Send(tgbotapi.NewMessage(chatID, "处理您的请求时发生内部错误，请稍后再试。"))
							}
						}
					}
				}()
				HandleUpdate(u, deps)
			}(update)

		case <-ctx.Done():
			logger.Info("Update processing loop stopped.")
			// (可选) 等待所有处理中的 goroutine 完成
			time.Sleep(10 * time.Second)
			return
		}
	}
}
