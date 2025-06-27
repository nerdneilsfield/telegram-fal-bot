package storage

import (
	"context" // Use context for cancellation/timeouts
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time" // Keep for timestamps

	"go.uber.org/zap"
	// Remove GORM imports:
	// "gorm.io/gorm"
	// "gorm.io/gorm/clause"
)

// SQLBalanceManager uses database/sql to manage user balances
type SQLBalanceManager struct {
	db      *sql.DB    // Standard sql.DB connection pool
	initial float64    // Initial balance
	cost    float64    // Cost per generation
	mu      sync.Mutex // Mutex for write operations (transactions handle atomicity)
}

// NewSQLBalanceManager creates a new SQLBalanceManager
func NewSQLBalanceManager(db *sql.DB, initialBalance, costPerGeneration float64) *SQLBalanceManager {
	return &SQLBalanceManager{
		db:      db,
		initial: initialBalance,
		cost:    costPerGeneration,
	}
}

// GetCost returns the cost per generation
func (bm *SQLBalanceManager) GetCost() float64 {
	return bm.cost
}

// GetBalance retrieves the balance for a user. Returns initial balance if user not found.
func (bm *SQLBalanceManager) GetBalance(userID int64) float64 {
	var balance float64
	query := `SELECT balance FROM user_balances WHERE user_id = ?`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Add timeout
	defer cancel()

	err := bm.db.QueryRowContext(ctx, query, userID).Scan(&balance)

	if err == nil {
		// User found
		return balance
	} else if errors.Is(err, sql.ErrNoRows) {
		// User not found, return initial balance
		return bm.initial
	} else {
		// Other database error
		zap.L().Error("Failed to query balance", zap.Int64("user_id", userID), zap.Error(err))
		// Return initial balance on error to avoid blocking usage
		return bm.initial
	}
}

// CheckAndDeduct checks if balance is sufficient and deducts the cost atomically.
// Creates the user record if it doesn't exist.
func (bm *SQLBalanceManager) CheckAndDeduct(userID int64) (bool, error) {
	if bm.cost <= 0 {
		zap.L().Info("Balance deduction skipped (cost <= 0)", zap.Int64("user_id", userID))
		return true, nil // Cost is zero or negative, always succeed
	}

	bm.mu.Lock()
	defer bm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Context for transaction
	defer cancel()

	tx, err := bm.db.BeginTx(ctx, nil) // Start transaction
	if err != nil {
		return false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // Rollback if anything fails before commit

	// 1. Try to get the current balance within the transaction (locks the row)
	var currentBalance sql.NullFloat64 // Use NullFloat64 to detect non-existence
	// Use SELECT ... FOR UPDATE if supported and needed for stricter locking,
	// but SQLite's default locking with transactions is often sufficient.
	// Let's keep it simple first.
	selectQuery := `SELECT balance FROM user_balances WHERE user_id = ?`
	err = tx.QueryRowContext(ctx, selectQuery, userID).Scan(&currentBalance)

	balanceToUse := bm.initial // Assume initial balance if not found

	if err == nil && currentBalance.Valid {
		// User exists
		balanceToUse = currentBalance.Float64
	} else if !errors.Is(err, sql.ErrNoRows) {
		// Error other than not found
		return false, fmt.Errorf("database error checking balance: %w", err)
	}
	// If err is sql.ErrNoRows, balanceToUse remains bm.initial

	// 2. Check if sufficient balance
	if balanceToUse < bm.cost {
		return false, fmt.Errorf("insufficient balance (%.2f), need %.2f", balanceToUse, bm.cost)
	}

	// 3. Calculate new balance
	newBalance := balanceToUse - bm.cost

	// 4. Upsert (Update or Insert) the balance
	// SQLite specific UPSERT syntax
	upsertSQL := `
		INSERT INTO user_balances (user_id, balance, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			balance = excluded.balance,
			updated_at = excluded.updated_at;`
	now := time.Now()
	_, err = tx.ExecContext(ctx, upsertSQL, userID, newBalance, now, now)
	if err != nil {
		return false, fmt.Errorf("failed to upsert user balance: %w", err)
	}

	// 5. Commit transaction
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit transaction: %w", err)
	}

	zap.L().Info("Balance deducted successfully", zap.Int64("user_id", userID), zap.Float64("new_balance", newBalance))
	return true, nil
}

// AddBalance adds the specified amount to the user's balance atomically.
func (bm *SQLBalanceManager) AddBalance(userID int64, amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be positive")
	}

	bm.mu.Lock()
	defer bm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Context for transaction
	defer cancel()

	tx, err := bm.db.BeginTx(ctx, nil) // Start transaction
	if err != nil {
		return fmt.Errorf("failed to begin transaction for add balance: %w", err)
	}
	defer tx.Rollback() // Rollback if anything fails before commit

	// 1. Get current balance or assume initial if not exists (within transaction)
	var currentBalance sql.NullFloat64
	selectQuery := `SELECT balance FROM user_balances WHERE user_id = ?`
	err = tx.QueryRowContext(ctx, selectQuery, userID).Scan(&currentBalance)

	balanceToUse := bm.initial // Assume initial balance if not found

	if err == nil && currentBalance.Valid {
		balanceToUse = currentBalance.Float64
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("database error checking balance on add: %w", err)
	}

	// 2. Calculate new balance
	newBalance := balanceToUse + amount

	// 3. Upsert the balance
	upsertSQL := `
		INSERT INTO user_balances (user_id, balance, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			balance = excluded.balance,
			updated_at = excluded.updated_at;`
	now := time.Now()
	_, err = tx.ExecContext(ctx, upsertSQL, userID, newBalance, now, now)
	if err != nil {
		return fmt.Errorf("failed to upsert user balance on add: %w", err)
	}

	// 4. Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction on add: %w", err)
	}

	zap.L().Info("Added balance for user", zap.Int64("user_id", userID), zap.Float64("amount", amount), zap.Float64("new_balance", newBalance))
	return nil
}

// SetBalance sets the balance for a user to a specific amount (admin function)
func (bm *SQLBalanceManager) SetBalance(userID int64, balance float64) error {
	if balance < 0 {
		return fmt.Errorf("balance cannot be negative")
	}

	bm.mu.Lock()
	defer bm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Upsert the balance directly
	upsertSQL := `
		INSERT INTO user_balances (user_id, balance, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			balance = excluded.balance,
			updated_at = excluded.updated_at;`
	now := time.Now()
	_, err := bm.db.ExecContext(ctx, upsertSQL, userID, balance, now, now)
	if err != nil {
		return fmt.Errorf("failed to set user balance: %w", err)
	}

	zap.L().Info("Set balance for user", zap.Int64("user_id", userID), zap.Float64("balance", balance))
	return nil
}

// UserBalance represents a user's balance information
type UserBalanceInfo struct {
	UserID    int64
	Balance   float64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListAllUsersWithBalances returns all users with their current balances
func (bm *SQLBalanceManager) ListAllUsersWithBalances() ([]UserBalanceInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	query := `SELECT user_id, balance, created_at, updated_at FROM user_balances ORDER BY user_id`
	rows, err := bm.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query user balances: %w", err)
	}
	defer rows.Close()

	var users []UserBalanceInfo
	for rows.Next() {
		var user UserBalanceInfo
		err := rows.Scan(&user.UserID, &user.Balance, &user.CreatedAt, &user.UpdatedAt)
		if err != nil {
			zap.L().Error("Failed to scan user balance row", zap.Error(err))
			continue
		}
		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating user balances: %w", err)
	}

	return users, nil
}
