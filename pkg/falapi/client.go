package falapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	cfg "github.com/nerdneilsfield/telegram-fal-bot/internal/config"
	loggerPkg "github.com/nerdneilsfield/telegram-fal-bot/internal/logger"
	"go.uber.org/zap"
)

type Client struct {
	apiKey      string
	httpClient  *http.Client
	captionURL  string
	generateURL string
	logger      *zap.Logger
}

func NewClient(config *cfg.Config) *Client {
	logger, err := loggerPkg.InitLogger(config.LogConfig.Level, config.LogConfig.Format, config.LogConfig.File)
	if err != nil {
		panic(fmt.Sprintf("Logger not initialized: %v", err))
	}
	return &Client{
		logger:      logger,
		apiKey:      config.FalAIKey,
		httpClient:  &http.Client{Timeout: 120 * time.Second}, // API 可能耗时较长
		captionURL:  config.APIEndpoints.FlorenceCaption,
		generateURL: config.APIEndpoints.FluxLora,
	}
}

// 内部方法用于执行请求
func (c *Client) doPostRequest(url string, payload interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		c.logger.Error("failed to marshal payload", zap.Error(err))
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		c.logger.Warn("failed to create request", zap.Error(err))
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Key "+c.apiKey) // 注意认证格式，根据 fal.ai 文档调整
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Warn("failed to send request", zap.Error(err))
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Warn("failed to read response body", zap.Error(err))
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		c.logger.Warn("API request failed", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	c.logger.Debug("API request successful", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
	return body, nil
}
