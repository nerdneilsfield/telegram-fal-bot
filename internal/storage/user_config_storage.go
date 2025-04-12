package storage

import (
	"errors"

	// "github.com/winjeg/go-commons/log" // Remove unused/incorrect import
	"go.uber.org/zap" // Use zap logger consistent with the project
	"gorm.io/gorm"
	"gorm.io/gorm/clause" // Add import for GORM clauses
)

// GetUserGenerationConfig 从数据库获取用户的生成配置
// 如果用户没有设置过配置，则返回 gorm.ErrRecordNotFound
func GetUserGenerationConfig(db *gorm.DB, userID int64) (*UserGenerationConfig, error) {
	var config UserGenerationConfig
	result := db.First(&config, "user_id = ?", userID)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// Use zap logger
			zap.L().Debug("No generation config found for user", zap.Int64("userID", userID))
			return nil, gorm.ErrRecordNotFound // 明确返回未找到错误
		}
		// Use zap logger
		zap.L().Error("Failed to get user generation config from DB", zap.Error(result.Error), zap.Int64("userID", userID))
		return nil, result.Error
	}
	// Use zap logger
	zap.L().Debug("Successfully retrieved user generation config", zap.Int64("userID", userID), zap.Any("config", config))
	return &config, nil
}

// SetUserGenerationConfig 将用户的生成配置保存或更新到数据库
// 使用 Upsert (如果记录存在则更新，否则创建)
func SetUserGenerationConfig(db *gorm.DB, userID int64, config UserGenerationConfig) error {
	config.UserID = userID // 确保 UserID 已设置

	// Use zap logger
	zap.L().Debug("Attempting to set user generation config", zap.Int64("userID", userID), zap.Any("config", config))

	// 使用 GORM 的 Clauses(clause.OnConflict) 来实现 Upsert 功能
	// 如果 user_id 冲突，则更新指定的列
	// Prefix clause types with the package name 'clause.'
	result := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"image_size", "num_inference_steps", "guidance_scale", "num_images", "language", "updated_at"}),
	}).Create(&config)

	if result.Error != nil {
		// Use zap logger
		zap.L().Error("Failed to set user generation config in DB", zap.Error(result.Error), zap.Int64("userID", userID))
		return result.Error
	}

	// Use zap logger
	zap.L().Info("Successfully set user generation config", zap.Int64("userID", userID), zap.Int64("rowsAffected", result.RowsAffected))
	return nil
}
