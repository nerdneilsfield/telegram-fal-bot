package bot

import (
	"database/sql"
	"errors"

	st "github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
	"go.uber.org/zap"
)

// GetUserVisibleLoras determines which LoRAs are visible to a specific user based on config.
func GetUserVisibleLoras(userID int64, deps BotDeps) []LoraConfig {
	// Admins see all standard LoRAs defined in the main list
	if deps.Authorizer.IsAdmin(userID) {
		return deps.LoRA
	}

	// If config is nil or sections are missing, return empty (or handle error)
	if deps.Config == nil {
		deps.Logger.Error("Config is nil in GetUserVisibleLoras")
		return []LoraConfig{}
	}

	// 1. Find all groups the user belongs to
	userGroupSet := make(map[string]struct{}) // Use a set for efficient lookup
	for _, group := range deps.Config.UserGroups {
		for _, id := range group.UserIDs {
			if id == userID {
				userGroupSet[group.Name] = struct{}{}
				break // User found in this group, move to next group
			}
		}
	}

	// 2. Filter LoRAs based on AllowGroups
	visibleLoras := []LoraConfig{}
	for _, lora := range deps.LoRA { // Iterate through standard LoRAs
		// Case 1: AllowGroups is empty - LoRA is public to all authorized users
		if len(lora.AllowGroups) == 0 {
			visibleLoras = append(visibleLoras, lora)
			continue // This LoRA is visible, move to the next one
		}

		// Case 2: AllowGroups is not empty - check if user is in any allowed group
		userHasAccess := false
		for _, allowedGroup := range lora.AllowGroups {
			if _, userInGroup := userGroupSet[allowedGroup]; userInGroup {
				userHasAccess = true
				break // User is in one of the allowed groups, grant access
			}
		}

		if userHasAccess {
			visibleLoras = append(visibleLoras, lora)
		}
	}

	// Note: BaseLoRAs are handled separately (e.g., only shown/selectable by admins)
	// If BaseLoRAs should also follow AllowGroups logic, that needs to be integrated here or handled distinctly.

	return visibleLoras
}

// Helper to find LoraConfig by ID (used in callback)
func findLoraByID(loraID string, allLoras []LoraConfig) LoraConfig {
	for _, lora := range allLoras {
		if lora.ID == loraID {
			return lora
		}
	}
	// Also check BaseLoRA if needed, or handle separately
	// for _, lora := range deps.BaseLoRA { ... }
	return LoraConfig{} // Return empty if not found
}

// findLoraByName searches a list of LoraConfig for a LoRA by its name.
// Returns the LoraConfig and a boolean indicating if it was found.
func findLoraByName(name string, loras []LoraConfig) (LoraConfig, bool) {
	for _, lora := range loras {
		if lora.Name == name {
			return lora, true
		}
	}
	return LoraConfig{}, false
}

// getUserLanguagePreference retrieves the user's preferred language code.
// Returns nil if no preference is set or an error occurs, allowing fallback to default.
func getUserLanguagePreference(userID int64, deps BotDeps) *string {
	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	if err != nil {
		// Check for sql.ErrNoRows specifically
		if !errors.Is(err, sql.ErrNoRows) {
			// Log other errors but don't block, will fallback to default language
			deps.Logger.Error("Failed to get user config for language preference",
				zap.Int64("user_id", userID),
				zap.Error(err))
		}
		// User config not found or other error, fallback to default
		deps.Logger.Debug("No user config found or error occurred, using default language", zap.Int64("user_id", userID), zap.Error(err))
		return nil
	}

	// userCfg is non-nil here (found in DB)
	// Check if the Language string field is non-empty
	if userCfg.Language != "" {
		deps.Logger.Debug("Found user language preference", zap.Int64("user_id", userID), zap.String("language", userCfg.Language))
		// Return pointer to the string value
		return &userCfg.Language
	}

	deps.Logger.Debug("User has no language preference set in config, using default", zap.Int64("user_id", userID))
	return nil // Preference field is empty string, fallback to default
}

// Helper to get user groups (can be moved to a more suitable place like auth or utils)
func GetUserGroups(userID int64, deps BotDeps) map[string]struct{} {
	userGroupSet := make(map[string]struct{})
	if deps.Config == nil || deps.Config.UserGroups == nil {
		return userGroupSet // Return empty set if config is missing
	}
	for _, group := range deps.Config.UserGroups {
		for _, id := range group.UserIDs {
			if id == userID {
				userGroupSet[group.Name] = struct{}{}
				break
			}
		}
	}
	return userGroupSet
}

// Helper to truncate long request IDs for display
func truncateID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}
