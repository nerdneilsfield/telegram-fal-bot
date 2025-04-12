package bot

import (
	// Import database/sql
	"fmt" // Added for panic message
	"regexp"
	"strings"

	"github.com/nerdneilsfield/telegram-fal-bot/internal/auth"
	// "github.com/nerdneilsfield/telegram-fal-bot/internal/balance" // Commented out
	"github.com/nerdneilsfield/telegram-fal-bot/internal/config"
	"github.com/nerdneilsfield/telegram-fal-bot/internal/i18n"
	"github.com/nerdneilsfield/telegram-fal-bot/internal/logger" // Import logger package

	"github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
	falapi "github.com/nerdneilsfield/telegram-fal-bot/pkg/falapi"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
	// Add gorm import if not already present
)

// Version and BuildDate are injected during build
var (
	Version   = "dev"
	BuildDate = "unknown"
)

// StartBot initializes and starts the Telegram bot.
// Corrected signature to accept config, version, buildDate
func StartBot(cfg *config.Config, version string, buildDate string) error {
	// Initialize Logger first, inside StartBot
	logger, err := logger.InitLogger(cfg.LogConfig.Level, cfg.LogConfig.Format, cfg.LogConfig.File)
	if err != nil {
		// Use fmt.Sprintf for panic as logger might not be initialized
		panic(fmt.Sprintf("Logger initialization failed: %v", err))
	}
	defer logger.Sync() // Ensure logs are flushed on exit

	logger.Info("Starting Telegram Bot...", zap.String("version", version), zap.String("buildDate", buildDate))

	// Initialize Bot API
	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		logger.Fatal("Failed to create bot", zap.Error(err))
	}
	// bot.Debug = cfg.TelegramDebug // Field missing
	logger.Info("Authorized on account", zap.String("username", bot.Self.UserName))

	// Initialize Fal Client (Pass the initialized logger)
	falClient, err := falapi.NewClient(
		cfg.FalAIKey,
		cfg.APIEndpoints.BaseURL,
		cfg.APIEndpoints.FluxLora,
		cfg.APIEndpoints.FlorenceCaption,
		logger.Named("fal_client"), // Pass named logger
	)
	if err != nil {
		logger.Fatal("Failed to initialize Fal client", zap.Error(err))
	}

	// Initialize i18n Manager (Pass the initialized logger)
	i18nManager, err := i18n.NewManager(cfg.DefaultLanguage, logger)
	if err != nil {
		logger.Fatal("Failed to initialize i18n manager", zap.Error(err))
	}

	// Initialize Database (Returns *sql.DB now)
	db, err := storage.InitDB(cfg.DBPath)
	if err != nil {
		logger.Fatal("Failed to initialize database", zap.Error(err))
	}
	// Defer closing the DB connection pool when StartBot exits
	// This might need adjustment based on application lifecycle
	// defer db.Close()

	// Initialize State Manager
	stateManager := NewStateManager()

	// Initialize Authorizer
	authorizer := auth.NewAuthorizer(cfg.Auth.AuthorizedUserIDs, cfg.Admins.AdminUserIDs)

	// Initialize Balance Manager (Optional)
	var balanceManager *storage.SQLBalanceManager // Use SQLBalanceManager
	if cfg.Balance.CostPerGeneration > 0 {
		// Use NewSQLBalanceManager
		balanceManager = storage.NewSQLBalanceManager(db, cfg.Balance.InitialBalance, cfg.Balance.CostPerGeneration)
		logger.Info("Balance tracking enabled")
	} else {
		logger.Info("Balance tracking disabled")
	}

	// Convert LoRA configs
	var botLoras []LoraConfig
	for _, cfgLora := range cfg.LoRAs {
		botLora, err := GenerateLoraConfig(cfgLora)
		if err != nil {
			logger.Error("Failed to process LoRA config", zap.String("name", cfgLora.Name), zap.Error(err))
			continue
		}
		botLoras = append(botLoras, botLora)
	}
	var botBaseLoras []LoraConfig
	for _, cfgLora := range cfg.BaseLoRAs {
		botLora, err := GenerateLoraConfig(cfgLora)
		if err != nil {
			logger.Error("Failed to process Base LoRA config", zap.String("name", cfgLora.Name), zap.Error(err))
			continue
		}
		botBaseLoras = append(botBaseLoras, botLora)
	}

	// Prepare dependencies (Pass the initialized logger)
	deps := BotDeps{
		Bot:            bot,
		FalClient:      falClient,
		DB:             db, // Pass the *sql.DB
		StateManager:   stateManager,
		Authorizer:     authorizer,
		BalanceManager: balanceManager, // Pass the *SQLBalanceManager
		I18n:           i18nManager,
		Logger:         logger, // Pass the logger initialized above
		Config:         cfg,
		LoRA:           botLoras,
		BaseLoRA:       botBaseLoras,
		Version:        version,   // Use passed-in version
		BuildDate:      buildDate, // Use passed-in buildDate
	}

	// Set bot commands (Pass the initialized logger)
	SetBotCommands(bot, logger, cfg.DefaultLanguage, deps.I18n)

	// Start update polling
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	logger.Info("Bot started, listening for updates...")
	for update := range updates {
		go func(upd tgbotapi.Update) {
			HandleUpdate(upd, deps)
		}(update)
	}

	return nil
}

// SetBotCommands defines the commands available to the user.
// Updated to accept default language string directly
func SetBotCommands(bot *tgbotapi.BotAPI, logger *zap.Logger, defaultLang string, i18nManager *i18n.Manager) {
	// Use the default language from config for command descriptions
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: i18nManager.T(&defaultLang, "command_desc_start")},
		{Command: "help", Description: i18nManager.T(&defaultLang, "command_desc_help")},
		{Command: "loras", Description: i18nManager.T(&defaultLang, "command_desc_loras")},
		{Command: "myconfig", Description: i18nManager.T(&defaultLang, "command_desc_myconfig")},
		{Command: "balance", Description: i18nManager.T(&defaultLang, "command_desc_balance")},
		{Command: "version", Description: i18nManager.T(&defaultLang, "command_desc_version")},
		{Command: "cancel", Description: i18nManager.T(&defaultLang, "command_desc_cancel")},
		{Command: "set", Description: i18nManager.T(&defaultLang, "command_desc_set")},
		{Command: "log", Description: i18nManager.T(&defaultLang, "command_desc_log")},
		{Command: "shortlog", Description: i18nManager.T(&defaultLang, "command_desc_shortlog")},
	}

	commandsConfig := tgbotapi.NewSetMyCommands(commands...)
	if _, err := bot.Request(commandsConfig); err != nil {
		logger.Error("Failed to set bot commands", zap.Error(err))
	} else {
		logger.Info("Successfully set bot commands")
	}
}

// GenerateLoraConfig sanitizes and prepares a LoraConfig for bot internal use.
func GenerateLoraConfig(lora config.LoraConfig) (LoraConfig, error) {
	// Sanitize Name to create ID
	id := strings.ToLower(lora.Name)
	id = strings.ReplaceAll(id, " ", "_")
	// Define a regex to keep only alphanumeric and underscores
	reg := regexp.MustCompile("[^a-z0-9_]+")
	id = reg.ReplaceAllString(id, "")
	// Remove leading/trailing/multiple underscores
	id = strings.Trim(id, "_")
	for strings.Contains(id, "__") {
		id = strings.ReplaceAll(id, "__", "_")
	}

	if id == "" {
		return LoraConfig{}, fmt.Errorf("generated empty ID for LoRA name: %s", lora.Name)
	}

	// Ensure ID length + prefix length ("lora_select_") <= 64 bytes
	const prefixLength = 12 // len("lora_select_")
	const maxCallbackDataLength = 64
	maxIDLength := maxCallbackDataLength - prefixLength // 52
	if len(id) > maxIDLength {
		id = id[:maxIDLength]
		// Consider logging a warning if a logger is available here
	}

	// Return the bot.LoraConfig with only the defined fields
	return LoraConfig{
		ID:          id, // Use sanitized and truncated ID
		Name:        lora.Name,
		URL:         lora.URL,         // Field exists in config.LoraConfig
		Weight:      lora.Weight,      // Field exists in config.LoraConfig
		AllowGroups: lora.AllowGroups, // Field exists in config.LoraConfig
		// BaseLoraOnly seems to be missing from config.LoraConfig, remove if necessary
		// BaseLoraOnly: lora.BaseLoraOnly, // Assuming this exists, otherwise remove
	}, nil
}
