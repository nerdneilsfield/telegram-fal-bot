package storage

import (
	"fmt"
	// 替换为你的 logger 包路径
	"log"  // Standard log for GORM logger config
	"os"   // For standard log flags
	"time" // For GORM logger config

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger" // Import GORM logger interface
)

// InitDB 初始化数据库连接并运行迁移
func InitDB(dbPath string) (*gorm.DB, error) {
	// 配置 GORM 日志记录器 (可选, 但推荐)
	newLogger := gormlogger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
		gormlogger.Config{
			SlowThreshold:             time.Second,     // Slow SQL threshold
			LogLevel:                  gormlogger.Warn, // Log level (Silent, Error, Warn, Info)
			IgnoreRecordNotFoundError: true,            // Ignore ErrRecordNotFound error for logger
			Colorful:                  true,            // Enable color
		},
	)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: newLogger, // Use configured logger
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	// 运行自动迁移
	zap.L().Info("Running database migrations...")
	err = db.AutoMigrate(&UserBalance{}, &UserGenerationConfig{}) // 添加 UserGenerationConfig
	if err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}
	zap.L().Info("Database migration completed.")

	return db, nil
}
