package storage

import (
	// "database/sql" // No longer needed directly here
	"fmt"
	// 替换为你的 logger 包路径
	"log"  // Standard log for GORM logger config
	"os"   // For standard log flags
	"time" // For GORM logger config

	"go.uber.org/zap"
	"gorm.io/driver/sqlite" // Keep the GORM SQLite driver import
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger" // Import GORM logger interface
	_ "modernc.org/sqlite"           // Import the pure Go SQLite driver
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

	// Use the GORM sqlite driver's Open function.
	// It should automatically use the registered "sqlite" driver (modernc.org/sqlite)
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: newLogger, // Use configured logger
	})

	// // Use the generic GORM Open with the driver name "sqlite" // Previous attempt
	// sqlDB, err := sql.Open("sqlite", dbPath)
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	// }

	// db, err := gorm.Open(gorm.Dialector{ // Pass the driver name and existing connection // Previous attempt
	// 	Name:           "sqlite",
	// 	Conn:           sqlDB,
	// 	SkipInitialize: true, // Prevent GORM from trying to initialize again
	// }, &gorm.Config{
	// 	Logger: newLogger, // Use configured logger
	// })

	// db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{ // Original line
	// 	Logger: newLogger, // Use configured logger
	// })
	if err != nil {
		// // Ensure the underlying sql.DB is closed if gorm.Open fails // Previous attempt cleanup
		// _ = sqlDB.Close()
		return nil, fmt.Errorf("failed to connect database: %w", err) // Reverted error message
	}

	// 运行自动迁移
	zap.L().Info("Running database migrations...")
	err = db.AutoMigrate(&UserBalance{}, &UserGenerationConfig{}) // Add the new model here
	// Add other models here if you create them

	if err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}
	zap.L().Info("Database migration completed.")

	return db, nil
}
