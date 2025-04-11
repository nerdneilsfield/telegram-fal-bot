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
