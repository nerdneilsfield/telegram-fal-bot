package bot

import (
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/nerdneilsfield/telegram-fal-bot/internal/auth"
	// No balance import needed here, storage is used
	cfg "github.com/nerdneilsfield/telegram-fal-bot/internal/config"
	"github.com/nerdneilsfield/telegram-fal-bot/internal/i18n"

	// Remove state import as state.go is in the same package
	// "github.com/nerdneilsfield/telegram-fal-bot/internal/state"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	st "github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
	fapi "github.com/nerdneilsfield/telegram-fal-bot/pkg/falapi"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// LoraConfig represents the configuration for a single LoRA, including a generated ID.
// This definition is within the bot package.
type LoraConfig struct {
	ID          string   // Unique ID generated from Name, URL, Weight
	Name        string   // Copied from config.LoraConfig
	URL         string   // Copied from config.LoraConfig
	Weight      float64  // Copied from config.LoraConfig
	AllowGroups []string // Copied from config.LoraConfig
}

// UserState holds the current state of a user interaction.
type UserState struct {
	UserID               int64    `json:"user_id"`
	ChatID               int64    `json:"chat_id"`                 // Original chat where interaction started
	MessageID            int      `json:"message_id"`              // ID of the message to edit (e.g., the keyboard message)
	Action               string   `json:"action"`                  // e.g., "awaiting_lora_selection", "awaiting_caption_confirmation"
	OriginalCaption      string   `json:"original_caption"`        // The text prompt or generated caption
	SelectedLoras        []string `json:"selected_loras"`          // Names of selected standard LoRAs
	SelectedBaseLoraName string   `json:"selected_base_lora_name"` // Name of the selected Base LoRA
	LastUpdated          time.Time
	// For config updates
	ConfigFieldToUpdate string
	ImageFileURL        string `json:"-"` // Store image URL if interaction started with photo
}

// BotDeps holds the dependencies required by the bot handlers.
type BotDeps struct {
	Bot            *tgbotapi.BotAPI
	FalClient      *fapi.Client
	DB             *gorm.DB
	StateManager   *StateManager // Correct type within the same package
	Authorizer     *auth.Authorizer
	BalanceManager *st.GormBalanceManager // Use storage.GormBalanceManager pointer
	I18n           *i18n.Manager
	Logger         *zap.Logger
	Config         *cfg.Config
	LoRA           []LoraConfig // Use bot.LoraConfig (with ID)
	BaseLoRA       []LoraConfig // Use bot.LoraConfig (with ID)
	Version        string
	BuildDate      string
}

// GenerateIDWithBlake2b generates a unique ID based on string and float inputs using Blake2b hashing.
func GenerateIDWithBlake2b(s1, s2 string, f float64) (string, error) {
	hash, err := blake2b.New256(nil) // Using Blake2b-256
	if err != nil {
		return "", fmt.Errorf("failed to create hash: %w", err)
	}
	dataString := fmt.Sprintf("%s|%s|%.6f", s1, s2, f) // Combine inputs into a unique string
	_, err = hash.Write([]byte(dataString))
	if err != nil {
		return "", fmt.Errorf("failed to write data to hash: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateLoraConfig converts a config.LoraConfig (from file) into a bot.LoraConfig (with runtime ID).
func GenerateLoraConfig(loraCfg cfg.LoraConfig) (LoraConfig, error) {
	id, err := GenerateIDWithBlake2b(loraCfg.Name, loraCfg.URL, loraCfg.Weight)
	if err != nil {
		return LoraConfig{}, fmt.Errorf("生成 ID 失败 for %s: %w", loraCfg.Name, err)
	}

	return LoraConfig{ // Return bot.LoraConfig type
		ID:          id,
		Name:        loraCfg.Name,
		URL:         loraCfg.URL,
		Weight:      loraCfg.Weight,
		AllowGroups: loraCfg.AllowGroups,
	}, nil
}
