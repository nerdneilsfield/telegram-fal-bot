package bot

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	auth "github.com/nerdneilsfield/telegram-fal-bot/internal/auth"
	cfg "github.com/nerdneilsfield/telegram-fal-bot/internal/config"
	st "github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
	fapi "github.com/nerdneilsfield/telegram-fal-bot/pkg/falapi"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"golang.org/x/crypto/blake2b"
)

type LoraConfig struct {
	ID          string // Unique ID generated from Name, URL, Weight
	Name        string
	URL         string
	Weight      float64
	AllowGroups []string // New: List of group names allowed to access (copied from cfg.LoraConfig)
}

// BotDeps 包含 Bot 需要的所有依赖
type BotDeps struct {
	Bot            *tgbotapi.BotAPI
	FalClient      *fapi.Client
	Config         *cfg.Config
	DB             *gorm.DB               // 新增：数据库连接
	StateManager   *StateManager          // Optional
	BalanceManager *st.GormBalanceManager // Optional
	Version        string
	BuildDate      string
	Authorizer     *auth.Authorizer
	BaseLoRA       []LoraConfig
	LoRA           []LoraConfig
	Logger         *zap.Logger
}

func GenerateIDWithBlake2b(s1, s2 string, f float64) (string, error) {
	// 创建一个新的 BLAKE2b-128 hash.Hash 对象。
	// 第一个参数是哈希的输出大小（字节），这里是 16 字节 (128 位)。
	// 第二个参数是可选的 key，如果不需要 keyed hashing，则为 nil。
	// 注意：blake2b.New(size, key) 返回的是 hash.Hash 接口。
	// 为了更明确地使用 128 位，可以直接调用 blake2b.New128(nil)。
	h, err := blake2b.New(16, nil) // 直接创建 128-bit (16-byte) hasher
	if err != nil {
		return "", fmt.Errorf("创建 blake2b-128 hasher 失败: %w", err)
	}

	// 写入字符串数据 (UTF-8 编码)
	// 写入顺序必须固定
	_, err = h.Write([]byte(s1))
	if err != nil {
		return "", fmt.Errorf("写入 s1 失败: %w", err)
	}
	_, err = h.Write([]byte(s2))
	if err != nil {
		return "", fmt.Errorf("写入 s2 失败: %w", err)
	}

	// 将 float64 转换为确定的字节表示 (使用 BigEndian)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, math.Float64bits(f))
	_, err = h.Write(buf)
	if err != nil {
		return "", fmt.Errorf("写入 float64 失败: %w", err)
	}

	// 计算 BLAKE2b-128 哈希值 (16 字节)
	hashBytes := h.Sum(nil) // 返回 16 字节

	// 将 16 字节的哈希值编码为 32 个字符的十六进制字符串
	id := hex.EncodeToString(hashBytes)

	return id, nil
}

func GenerateLoraConfig(loraCfg cfg.LoraConfig) (LoraConfig, error) {
	id, err := GenerateIDWithBlake2b(loraCfg.Name, loraCfg.URL, loraCfg.Weight)
	if err != nil {
		return LoraConfig{}, fmt.Errorf("生成 ID 失败 for %s: %w", loraCfg.Name, err)
	}

	return LoraConfig{
		ID:          id,
		Name:        loraCfg.Name,
		URL:         loraCfg.URL,
		Weight:      loraCfg.Weight,
		AllowGroups: loraCfg.AllowGroups, // Copy AllowGroups from config
	}, nil
}
