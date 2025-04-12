package storage

import (
	"time"
)

// UserBalance defines the database table structure for user balances.
// GORM tags are removed as they are no longer relevant.
type UserBalance struct {
	UserID    int64   // Telegram User ID as primary key
	Balance   float64 // User's current balance
	CreatedAt time.Time
	UpdatedAt time.Time
	// DeletedAt gorm.DeletedAt // Removed soft delete as we manage deletion manually
}

// UserGenerationConfig defines the database table structure for user-specific generation settings.
// Fields are now non-pointers as the DB schema has defaults and NOT NULL constraints.
// GORM tags are removed.
type UserGenerationConfig struct {
	UserID            int64   // Telegram User ID as primary key
	ImageSize         string  `json:"image_size"`
	NumInferenceSteps int     `json:"num_inference_steps"`
	GuidanceScale     float64 `json:"guidance_scale"`
	NumImages         int     `json:"num_images"`
	Language          string  `json:"language"` // User's language preference
	CreatedAt         time.Time
	UpdatedAt         time.Time
	// DeletedAt         gorm.DeletedAt // Removed soft delete
}
