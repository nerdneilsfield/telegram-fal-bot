package storage

import (
	"time"

	"gorm.io/gorm"
)

// UserBalance 定义了存储用户余额的数据库表结构
type UserBalance struct {
	UserID    int64          `gorm:"primaryKey"` // Telegram User ID 作为主键
	Balance   float64        `gorm:"not null;default:0"`
	CreatedAt time.Time      // GORM 会自动处理
	UpdatedAt time.Time      // GORM 会自动处理
	DeletedAt gorm.DeletedAt `gorm:"index"` // 支持软删除 (可选)
}

// UserGenerationConfig 定义了存储用户个性化生成设置的数据库表结构
type UserGenerationConfig struct {
	UserID            int64          `gorm:"primaryKey"`                    // Telegram User ID 作为主键
	ImageSize         *string        `json:"image_size,omitempty"`          // 使用指针以区分未设置和空字符串
	NumInferenceSteps *int           `json:"num_inference_steps,omitempty"` // 使用指针以区分未设置和 0
	GuidanceScale     *float64       `json:"guidance_scale,omitempty"`      // 使用指针以区分未设置和 0
	NumImages         *int           `json:"num_images,omitempty"`          // 使用指针以区分未设置和 0
	CreatedAt         time.Time      // GORM 会自动处理
	UpdatedAt         time.Time      // GORM 会自动处理
	DeletedAt         gorm.DeletedAt `gorm:"index"` // 支持软删除 (可选)
}
