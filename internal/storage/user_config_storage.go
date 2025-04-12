package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// "github.com/winjeg/go-commons/log" // Remove unused/incorrect import
	"go.uber.org/zap" // Use zap logger consistent with the project
	// Remove GORM imports
	// "gorm.io/gorm"
	// "gorm.io/gorm/clause"
)

// GetUserGenerationConfig retrieves the user's generation config from the database.
// Returns sql.ErrNoRows if the user has no config set.
// Handles potential NULL values from the database for non-pointer struct fields.
func GetUserGenerationConfig(db *sql.DB, userID int64) (*UserGenerationConfig, error) {
	query := `SELECT image_size, num_inference_steps, guidance_scale, num_images, language, created_at, updated_at
			  FROM user_generation_configs
			  WHERE user_id = ?`

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use sql.Null types for scanning fields that might be NULL in the DB
	var imageSize sql.NullString
	var numSteps sql.NullInt64 // Changed to NullInt64
	var guidScale sql.NullFloat64
	var numImages sql.NullInt64 // Changed to NullInt64
	var language sql.NullString
	var createdAt sql.NullTime // Use NullTime for potential NULL timestamps
	var updatedAt sql.NullTime

	err := db.QueryRowContext(ctx, query, userID).Scan(
		&imageSize,
		&numSteps,
		&guidScale,
		&numImages,
		&language,
		&createdAt,
		&updatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			zap.L().Debug("No generation config found for user", zap.Int64("userID", userID))
			return nil, sql.ErrNoRows
		}
		zap.L().Error("Failed to get user generation config from DB", zap.Error(err), zap.Int64("userID", userID))
		return nil, fmt.Errorf("database error getting config: %w", err)
	}

	// Construct the config, assigning defaults if scanned values are NULL (Not Valid)
	config := &UserGenerationConfig{
		UserID: userID,
		// Assign default values explicitly if NULL or use the scanned value
		ImageSize:         "square_hd", // Provide a sensible default
		NumInferenceSteps: 30,          // Provide a sensible default
		GuidanceScale:     7.5,         // Provide a sensible default
		NumImages:         1,           // Provide a sensible default
		Language:          "",          // Default to empty, can be overridden by default language later
		CreatedAt:         time.Time{}, // Zero time if NULL
		UpdatedAt:         time.Time{}, // Zero time if NULL
	}

	if imageSize.Valid {
		config.ImageSize = imageSize.String
	}
	if numSteps.Valid {
		config.NumInferenceSteps = int(numSteps.Int64) // Convert from int64
	}
	if guidScale.Valid {
		config.GuidanceScale = guidScale.Float64
	}
	if numImages.Valid {
		config.NumImages = int(numImages.Int64) // Convert from int64
	}
	if language.Valid {
		config.Language = language.String
	}
	if createdAt.Valid {
		config.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		config.UpdatedAt = updatedAt.Time
	}

	zap.L().Debug("Successfully retrieved user generation config", zap.Int64("userID", userID), zap.Any("config", config))
	return config, nil
}

// SetUserGenerationConfig saves or updates the user's generation config in the database using UPSERT.
func SetUserGenerationConfig(db *sql.DB, config UserGenerationConfig) error {
	zap.L().Debug("Attempting to set user generation config", zap.Int64("userID", config.UserID), zap.Any("config", config))

	upsertSQL := `
		INSERT INTO user_generation_configs (user_id, image_size, num_inference_steps, guidance_scale, num_images, language, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			image_size = excluded.image_size,
			num_inference_steps = excluded.num_inference_steps,
			guidance_scale = excluded.guidance_scale,
			num_images = excluded.num_images,
			language = excluded.language,
			updated_at = excluded.updated_at;`

	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := db.ExecContext(ctx, upsertSQL,
		config.UserID,
		config.ImageSize,
		config.NumInferenceSteps,
		config.GuidanceScale,
		config.NumImages,
		config.Language, // Include language in insert/update
		now,             // created_at (only used on insert)
		now,             // updated_at
	)

	if err != nil {
		zap.L().Error("Failed to set user generation config in DB", zap.Error(err), zap.Int64("userID", config.UserID))
		return fmt.Errorf("database error setting config: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	zap.L().Info("Successfully set user generation config", zap.Int64("userID", config.UserID), zap.Int64("rowsAffected", rowsAffected))
	return nil
}
