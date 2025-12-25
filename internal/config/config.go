package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	BotToken                  string             `toml:"botToken"`
	FalAIKey                  string             `toml:"falAIKey"`
	TelegramAPIURL            string             `toml:"telegramAPIURL"`
	DBPath                    string             `toml:"dbPath"`
	BaseLoRAs                 []LoraConfig       `toml:"baseLoRAs"`
	LoRAs                     []LoraConfig       `toml:"loras"`
	LogConfig                 LogConfig          `toml:"logConfig"`
	APIEndpoints              APIEndpointsConfig `toml:"apiEndpoints"`
	Auth                      AuthConfig         `toml:"auth"`
	Admins                    AdminConfig        `toml:"admins"`
	Balance                   BalanceConfig      `toml:"balance"`
	DefaultGenerationSettings GenerationConfig   `toml:"defaultGenerationSettings"`
	UserGroups                []UserGroup        `toml:"userGroups"`
	DefaultLanguage           string             `toml:"defaultLanguage"`
}

type LogConfig struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
	File   string `toml:"file"`
}

type APIEndpointsConfig struct {
	BaseURL         string `toml:"baseURL"`
	FlorenceCaption string `toml:"florenceCaption"`
	FluxLora        string `toml:"fluxLora"`
}

type AuthConfig struct {
	AuthorizedUserIDs []int64 `toml:"authorizedUserIDs"`
}

type AdminConfig struct {
	AdminUserIDs []int64 `toml:"adminUserIDs"`
}

type LoraConfig struct {
	Name         string   `toml:"name"`
	URL          string   `toml:"url"`
	Weight       float64  `toml:"weight"`
	AllowGroups  []string `toml:"allowGroups,omitempty"`
	AppendPrompt string   `toml:"append_prompt"`
}

type BalanceConfig struct {
	InitialBalance    float64 `toml:"initialBalance"`
	CostPerGeneration float64 `toml:"costPerGeneration"`
}

type GenerationConfig struct {
	ImageSize         string  `toml:"imageSize" json:"image_size"`
	NumInferenceSteps int     `toml:"numInferenceSteps" json:"num_inference_steps"`
	GuidanceScale     float64 `toml:"guidanceScale" json:"guidance_scale"`
	NumImages         int     `toml:"numImages"`
}

type UserGroup struct {
	Name    string  `toml:"name"`
	UserIDs []int64 `toml:"userIDs"`
}

func LoadConfig(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func ValidateURL(urlString string) bool {
	if urlString == "" {
		return false
	}
	// check if the url is valid
	if _, err := url.Parse(urlString); err != nil {
		return false
	}
	return true
}

func MaskedPrint(str string) string {
	// only show the last 4 characters
	return strings.Repeat("*", len(str)-4) + str[len(str)-4:]
}

func PrintConfig(cfg *Config) {
	fmt.Println()
	fmt.Println("--------------------------------")
	fmt.Println("Config:")
	fmt.Printf("\tBotToken: %s\n", MaskedPrint(cfg.BotToken))
	fmt.Printf("\tFalAIKey: %s\n", MaskedPrint(cfg.FalAIKey))
	fmt.Printf("\tTelegramAPIURL: %s\n", cfg.TelegramAPIURL)
	fmt.Printf("\tDBPath: %s\n", cfg.DBPath)
	fmt.Printf("\tBaseLoRAs:\n")
	for _, lora := range cfg.BaseLoRAs {
		fmt.Printf("\t\t- Name: %s, URL: %s, Weight: %.2f, AllowGroups: %v\n", lora.Name, lora.URL, lora.Weight, lora.AllowGroups)
	}
	fmt.Printf("\tLoRAs:\n")
	for _, lora := range cfg.LoRAs {
		fmt.Printf("\t\t- Name: %s, URL: %s, Weight: %.2f, AllowGroups: %v\n", lora.Name, lora.URL, lora.Weight, lora.AllowGroups)
	}
	fmt.Printf("\tLogConfig: %v\n", cfg.LogConfig)
	fmt.Printf("\tAPIEndpoints: %v\n", cfg.APIEndpoints)
	fmt.Printf("\tAuth: %v\n", cfg.Auth)
	fmt.Printf("\tAdmins: %v\n", cfg.Admins)
	fmt.Printf("\tBalance: %v\n", cfg.Balance)
	fmt.Printf("\tDefaultGenerationSettings: %v\n", cfg.DefaultGenerationSettings)
	fmt.Printf("\tUserGroups: %v\n", cfg.UserGroups)
	fmt.Printf("\tDefaultLanguage: %s\n", cfg.DefaultLanguage)
	fmt.Println("--------------------------------")
	fmt.Println()
}

func ValidateConfig(cfg *Config) error {
	PrintConfig(cfg)
	if cfg.BotToken == "" {
		return fmt.Errorf("BotToken is required")
	}
	if cfg.FalAIKey == "" {
		return fmt.Errorf("falAIKey is required")
	}
	if cfg.TelegramAPIURL == "" || !ValidateURL(strings.ReplaceAll(cfg.TelegramAPIURL, "%s", cfg.BotToken)) {
		return fmt.Errorf("telegramAPIURL is required and must be a valid URL")
	}
	if cfg.APIEndpoints.FlorenceCaption == "" || !ValidateURL(cfg.APIEndpoints.FlorenceCaption) {
		return fmt.Errorf("APIEndpoints is required and must be a valid URL")
	}
	if cfg.APIEndpoints.FluxLora == "" || !ValidateURL(cfg.APIEndpoints.FluxLora) {
		return fmt.Errorf("fluxLora is required and must be a valid URL")
	}
	if len(cfg.Admins.AdminUserIDs) == 0 {
		return fmt.Errorf("adminUserIDs is required")
	}
	if len(cfg.Auth.AuthorizedUserIDs) == 0 {
		return fmt.Errorf("authorizedUserIDs is required")
	}
	if len(cfg.LoRAs) == 0 && len(cfg.BaseLoRAs) == 0 {
		return fmt.Errorf("at least one LoRA or BaseLoRA must be defined")
	}
	if cfg.Balance.InitialBalance <= 0 {
		return fmt.Errorf("initialBalance must be greater than 0")
	}
	if cfg.Balance.CostPerGeneration <= 0 {
		return fmt.Errorf("costPerGeneration must be greater than 0")
	}
	if cfg.DBPath == "" {
		return fmt.Errorf("dbPath is required")
	}
	if cfg.LogConfig.Level == "" {
		return fmt.Errorf("logLevel is required")
	}
	if cfg.LogConfig.Format == "" {
		return fmt.Errorf("logFormat is required")
	}
	if cfg.DefaultGenerationSettings.ImageSize == "" {
		return fmt.Errorf("imageSize is required")
	}
	if !(cfg.DefaultGenerationSettings.ImageSize == "portrait_16_9" || cfg.DefaultGenerationSettings.ImageSize == "square" || cfg.DefaultGenerationSettings.ImageSize == "landscape_16_9" || cfg.DefaultGenerationSettings.ImageSize == "landscape_4_3" || cfg.DefaultGenerationSettings.ImageSize == "portrait_4_3") {
		return fmt.Errorf("imageSize must be one of: portrait_16_9, square, landscape_16_9, landscape_4_3, portrait_4_3")
	}
	if cfg.DefaultGenerationSettings.NumInferenceSteps <= 0 || cfg.DefaultGenerationSettings.NumInferenceSteps > 50 {
		return fmt.Errorf("numInferenceSteps must be greater than 0 and less than 50")
	}
	if cfg.DefaultGenerationSettings.GuidanceScale < 0 || cfg.DefaultGenerationSettings.GuidanceScale > 15 {
		return fmt.Errorf("guidanceScale must be between 0 and 15")
	}
	if cfg.DefaultGenerationSettings.NumImages <= 0 {
		return fmt.Errorf("numImages must be positive")
	}
	if cfg.DefaultLanguage == "" {
		return fmt.Errorf("defaultLanguage is required")
	}

	groupNames := make(map[string]struct{})
	for _, group := range cfg.UserGroups {
		if group.Name == "" {
			return fmt.Errorf("user group name cannot be empty")
		}
		if _, exists := groupNames[group.Name]; exists {
			return fmt.Errorf("duplicate user group name found: %s", group.Name)
		}
		groupNames[group.Name] = struct{}{}
	}

	validateLoraList := func(loras []LoraConfig, listName string) error {
		loraNames := make(map[string]struct{})
		for _, lora := range loras {
			if lora.Name == "" {
				return fmt.Errorf("lora name in %s cannot be empty", listName)
			}
			if _, exists := loraNames[lora.Name]; exists {
				return fmt.Errorf("duplicate lora name found in %s: %s", listName, lora.Name)
			}
			loraNames[lora.Name] = struct{}{}

			if lora.URL == "" || !ValidateURL(lora.URL) {
				return fmt.Errorf("lora '%s' in %s has an invalid URL: %s", lora.Name, listName, lora.URL)
			}

			for _, allowedGroup := range lora.AllowGroups {
				if _, ok := groupNames[allowedGroup]; !ok {
					return fmt.Errorf("group '%s' in allowGroups for lora '%s' (list %s) does not exist in userGroups definition", allowedGroup, lora.Name, listName)
				}
			}
		}
		return nil
	}

	if err := validateLoraList(cfg.LoRAs, "loras"); err != nil {
		return err
	}
	if err := validateLoraList(cfg.BaseLoRAs, "baseLoRAs"); err != nil {
		return err
	}

	return nil
}
