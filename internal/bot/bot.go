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
	// Add gorm import if not already present
)

func StartBot(config *cfg.Config, version string, buildDate string) {
	// Initialize logger
	logger, err := loggerPkg.InitLogger(config.LogConfig.Level, config.LogConfig.Format, config.LogConfig.File)
	if err != nil {
		panic(fmt.Sprintf("Logger not initialized: %v", err))
	}

	logger.Info("Starting Telegram Bot...")

	// Initialize Database
	db, err := storage.InitDB(config.DBPath)
	if err != nil {
		logger.Fatal("Failed to initialize database", zap.Error(err))
	}
	logger.Info("Database initialized successfully")

	// Initialize Fal API Client
	falClient, err := fapi.NewClient(
		config.FalAIKey,
		config.APIEndpoints.BaseURL,
		config.APIEndpoints.FluxLora,
		config.APIEndpoints.FlorenceCaption,
		logger,
	)
	if err != nil {
		logger.Fatal("Failed to initialize Fal API client", zap.Error(err))
	}
	logger.Info("Fal API Client initialized successfully")

	// Initialize Telegram Bot API
	bot, err := tgbotapi.NewBotAPI(config.BotToken)
	if err != nil {
		logger.Fatal("Failed to create Bot API", zap.Error(err))
	}
	bot.Debug = false // Disable debug logging for the bot library unless needed
	logger.Info("Authorized on account", zap.String("username", bot.Self.UserName))

	// Set bot commands
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "开始使用 Bot"},
		{Command: "help", Description: "获取帮助信息"},
		{Command: "cancel", Description: "取消当前操作"},
		{Command: "balance", Description: "查询余额"},
		{Command: "loras", Description: "查看可用风格"},
		{Command: "version", Description: "查看版本信息"},
		{Command: "myconfig", Description: "查看/设置我的生成参数"},
		{Command: "set", Description: "(管理员) 管理用户组和Lora权限"},
	}
	commandsConfig := tgbotapi.NewSetMyCommands(commands...)
	if _, err := bot.Request(commandsConfig); err != nil {
		logger.Error("Failed to set bot commands", zap.Error(err))
	} else {
		logger.Info("Successfully set bot commands")
	}

	// Initialize State Manager
	stateManager := NewStateManager()

	// Initialize Authorizer
	authorizer := auth.NewAuthorizer(config.Auth.AuthorizedUserIDs, config.Admins.AdminUserIDs)
	logger.Info("Authorizer initialized")

	// Initialize Balance Manager (if configured)
	var balanceManager *storage.GormBalanceManager
	if config.Balance.CostPerGeneration > 0 { // Enable balance manager only if cost is configured
		balanceManager = storage.NewGormBalanceManager(db, config.Balance.InitialBalance, config.Balance.CostPerGeneration)
		logger.Info("Balance Manager initialized", zap.Float64("initial", config.Balance.InitialBalance), zap.Float64("cost", config.Balance.CostPerGeneration))
	} else {
		logger.Info("Balance Manager is disabled")
	}

	// Initialize LoRA configurations (assuming GenerateLoraConfig is defined elsewhere)
	baseLoraList := []LoraConfig{}
	for _, lora := range config.BaseLoRAs {
		loraConfig, err := GenerateLoraConfig(lora)
		if err != nil {
			logger.Fatal("Failed to generate base lora config", zap.Error(err), zap.String("name", lora.Name))
		}
		baseLoraList = append(baseLoraList, loraConfig)
	}

	loras := []LoraConfig{}
	for _, lora := range config.LoRAs {
		loraConfig, err := GenerateLoraConfig(lora)
		if err != nil {
			logger.Fatal("Failed to generate lora config", zap.Error(err), zap.String("name", lora.Name))
		}
		loras = append(loras, loraConfig)
	}

	// Create BotDeps with the DB connection
	deps := BotDeps{
		Bot:            bot,
		FalClient:      falClient,
		Config:         config,
		DB:             db, // Pass the initialized DB connection
		StateManager:   stateManager,
		BalanceManager: balanceManager,
		Authorizer:     authorizer,
		BaseLoRA:       baseLoraList,
		LoRA:           loras,
		Version:        version,
		BuildDate:      buildDate,
		Logger:         logger,
	}

	// Setup update channel and signal handling
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 120
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := bot.GetUpdatesChan(u)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		logger.Info("Received interrupt signal, shutting down...")
		cancel()
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
				HandleUpdate(u, deps) // Pass deps containing DB connection
			}(update)

		case <-ctx.Done():
			logger.Info("Update processing loop stopped.")
			// Optional graceful shutdown delay
			time.Sleep(5 * time.Second) // Reduced delay
			logger.Info("Exiting.")
			return
		}
	}
}
