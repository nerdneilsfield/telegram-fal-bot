package storage

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	// 替换
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormBalanceManager 使用 GORM 管理用户余额
type GormBalanceManager struct {
	db      *gorm.DB
	initial float64    // 初始余额，从配置读取
	cost    float64    // 每次生成成本，从配置读取
	mu      sync.Mutex // Add mutex for write operations
}

// NewGormBalanceManager 创建一个新的 GormBalanceManager
func NewGormBalanceManager(db *gorm.DB, initialBalance, costPerGeneration float64) *GormBalanceManager {
	return &GormBalanceManager{
		db:      db,
		initial: initialBalance,
		cost:    costPerGeneration,
	}
}

// GetBalance 获取指定用户的余额，如果用户不存在则返回初始余额
func (bm *GormBalanceManager) GetBalance(userID int64) float64 {
	var userBalance UserBalance
	result := bm.db.Where("user_id = ?", userID).First(&userBalance)

	if result.Error == nil {
		// 用户找到
		return userBalance.Balance
	} else if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		// 用户未找到，视为拥有初始余额
		return bm.initial
	} else {
		// 其他数据库错误
		zap.L().Error("Failed to query balance for get", zap.Int64("user_id", userID), zap.Error(result.Error))
		// 在数据库错误时也返回初始余额，避免用户无法使用
		return bm.initial
	}
}

// CheckAndDeduct 检查用户余额是否足够并扣除成本。
// 如果用户首次使用，会自动创建记录并扣除。
// 操作是原子性的（在事务中执行）。
func (bm *GormBalanceManager) CheckAndDeduct(userID int64) (bool, error) {
	bm.mu.Lock()         // Lock before starting the transaction
	defer bm.mu.Unlock() // Ensure unlock happens even on error/panic

	err := bm.db.Transaction(func(tx *gorm.DB) error {
		var userBalance UserBalance
		// 尝试查找用户，使用 Lock for update 保证事务隔离性
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).First(&userBalance)

		currentBalance := bm.initial // 假设用户不存在时的初始值

		if result.Error == nil {
			// 用户已存在
			currentBalance = userBalance.Balance
		} else if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// 查找时发生非"未找到"的数据库错误
			return fmt.Errorf("database error checking balance: %w", result.Error)
		}
		// 如果是 ErrRecordNotFound，currentBalance 保持为 bm.initial

		// 检查余额是否足够
		if currentBalance < bm.cost {
			return fmt.Errorf("insufficient balance (%.2f), need %.2f", currentBalance, bm.cost) // 返回特定错误类型可能更好
		}

		// 计算新余额
		newBalance := currentBalance - bm.cost

		// 更新或创建记录
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// 用户不存在，创建新记录
			newUser := UserBalance{
				UserID:  userID,
				Balance: newBalance, // 余额为 初始值 - 成本
			}
			if err := tx.Create(&newUser).Error; err != nil {
				return fmt.Errorf("failed to create user balance record: %w", err)
			}
			zap.L().Info("Created new balance record for user", zap.Int64("user_id", userID), zap.Float64("new_balance", newBalance))
		} else {
			// 用户存在，更新余额
			// 使用 Update 而不是 Save，只更新指定字段
			updateResult := tx.Model(&UserBalance{}).Where("user_id = ?", userID).Update("balance", newBalance)
			if updateResult.Error != nil {
				return fmt.Errorf("failed to update user balance: %w", updateResult.Error)
			}
			if updateResult.RowsAffected == 0 {
				// 可能由于并发问题或其他原因没有更新成功 (虽然事务+Lock应该避免大部分情况)
				// return fmt.Errorf("failed to update balance, zero rows affected for user %d", userID)
				zap.L().Warn("Zero rows affected when updating balance", zap.Int64("user_id", userID)) // 记录但不一定视为失败
			}
		}

		return nil // 事务成功提交
	}) // 事务结束

	if err != nil {
		// 事务执行失败 (可能是数据库错误，也可能是我们返回的余额不足错误)
		// 记录错误，但不一定是致命的（比如余额不足就是正常流程）
		if !strings.Contains(err.Error(), "insufficient balance") { // 避免记录余额不足为错误日志
			zap.L().Error("Balance deduction transaction failed", zap.Int64("user_id", userID), zap.Error(err))
		}
		return false, err // 将错误传递回去
	}

	return true, nil // 事务成功，扣费完成
}

// AddBalance 为用户增加余额 (例如用于管理员充值)
func (bm *GormBalanceManager) AddBalance(userID int64, amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be positive")
	}

	bm.mu.Lock()         // Lock before starting the transaction
	defer bm.mu.Unlock() // Ensure unlock happens even on error/panic

	err := bm.db.Transaction(func(tx *gorm.DB) error {
		var userBalance UserBalance
		// 查找用户，同样加锁
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", userID).First(&userBalance)

		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			// 用户不存在，创建并设置余额为 initial + amount
			newUser := UserBalance{
				UserID:  userID,
				Balance: bm.initial + amount,
			}
			if err := tx.Create(&newUser).Error; err != nil {
				return fmt.Errorf("failed to create user balance record on add: %w", err)
			}
			zap.L().Info("Created new balance record via AddBalance", zap.Int64("user_id", userID), zap.Float64("new_balance", newUser.Balance))
		} else if result.Error != nil {
			// 其他数据库错误
			return fmt.Errorf("database error checking balance on add: %w", result.Error)
		} else {
			// 用户存在，增加余额
			newBalance := userBalance.Balance + amount
			if err := tx.Model(&UserBalance{}).Where("user_id = ?", userID).Update("balance", newBalance).Error; err != nil {
				return fmt.Errorf("failed to update user balance on add: %w", err)
			}
			zap.L().Info("Added balance for user", zap.Int64("user_id", userID), zap.Float64("amount", amount), zap.Float64("new_balance", newBalance))
		}
		return nil // 事务成功
	})
	return err // 返回事务的最终结果
}
