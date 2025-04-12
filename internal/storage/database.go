package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time" // Keep for potential future use or logging

	"go.uber.org/zap"
	_ "modernc.org/sqlite" // Import the pure Go SQLite driver
)

const (
	createUserBalanceTableSQL = `
	CREATE TABLE IF NOT EXISTS user_balances (
		user_id INTEGER PRIMARY KEY,
		balance REAL NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);`

	createUserGenerationConfigTableSQL = `
	CREATE TABLE IF NOT EXISTS user_generation_configs (
		user_id INTEGER PRIMARY KEY,
		image_size TEXT NOT NULL DEFAULT 'square_hd',
		num_inference_steps INTEGER NOT NULL DEFAULT 30,
		guidance_scale REAL NOT NULL DEFAULT 7.5,
		num_images INTEGER NOT NULL DEFAULT 1,
		language TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);`

	// Add indexes for potentially frequent lookups
	createUserIDIndexBalanceSQL = `CREATE INDEX IF NOT EXISTS idx_user_balances_user_id ON user_balances (user_id);`
	createUserIDIndexConfigSQL  = `CREATE INDEX IF NOT EXISTS idx_user_generation_configs_user_id ON user_generation_configs (user_id);`

	// Add migration step for the language column
	addLanguageColumnSQL = `
	ALTER TABLE user_generation_configs
	ADD COLUMN language TEXT NOT NULL DEFAULT '';`
)

// InitDB initializes the database connection using database/sql and runs migrations.
func InitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// Configure connection pool (optional but recommended)
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Ping database to ensure connection is valid
	if err := db.Ping(); err != nil {
		db.Close() // Close the connection if ping fails
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Run migrations manually
	zap.L().Info("Running database migrations...")
	if err := runMigrations(db); err != nil {
		db.Close() // Close the connection if migrations fail
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}
	zap.L().Info("Database migration completed.")

	return db, nil
}

// runMigrations executes the necessary SQL statements to create/update tables.
func runMigrations(db *sql.DB) error {
	// Statements to ensure tables and indexes exist
	initialStatements := []string{
		createUserBalanceTableSQL,
		createUserGenerationConfigTableSQL,
		createUserIDIndexBalanceSQL,
		createUserIDIndexConfigSQL,
	}

	for _, stmt := range initialStatements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("failed to execute initial migration statement: %w\nSQL: %s", err, stmt)
		}
	}

	// Attempt to add the language column. Ignore error if column already exists.
	// NOTE: A more robust migration system would track applied migrations.
	// This simple approach works for adding a single column.
	zap.L().Info("Attempting to add 'language' column to user_generation_configs table...")
	if _, err := db.Exec(addLanguageColumnSQL); err != nil {
		// Check if the error is specifically about the column already existing.
		// SQLite error message for duplicate column might vary, but often contains "duplicate column name".
		if !isDuplicateColumnError(err) {
			zap.L().Error("Failed to add 'language' column (unexpected error)", zap.Error(err))
			// Decide if this should be a fatal error. For now, log and continue.
			// return fmt.Errorf("failed to execute add column statement: %w\nSQL: %s", err, addLanguageColumnSQL)
		} else {
			zap.L().Info("'language' column likely already exists.")
		}
	} else {
		zap.L().Info("'language' column added successfully or already existed.")
	}

	return nil
}

// isDuplicateColumnError checks if an error message indicates a duplicate column.
// This is a basic check and might need adjustment based on specific SQLite versions/drivers.
func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	// Error messages like "duplicate column name: language" or similar
	return strings.Contains(err.Error(), "duplicate column name") || strings.Contains(err.Error(), "already exists")
}
