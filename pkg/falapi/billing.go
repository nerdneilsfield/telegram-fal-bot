package falapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go.uber.org/zap"
	// ... other imports ...
)

func (c *Client) GetAccountBalance() (float64, error) {
	balanceURL := "https://rest.alpha.fal.ai/billing/user_balance" // Confirmed URL

	req, err := http.NewRequest("GET", balanceURL, nil)
	if err != nil {
		c.logger.Error("failed to create account balance request", zap.Error(err))
		return 0, fmt.Errorf("failed to create account balance request: %w", err)
	}
	req.Header.Set("Authorization", "Key "+c.apiKey)
	req.Header.Set("Accept", "application/json") // Still expect JSON content type

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("failed to send account balance request", zap.Error(err))
		return 0, fmt.Errorf("failed to send account balance request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("failed to read account balance response body", zap.Error(err))
		return 0, fmt.Errorf("failed to read account balance response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Even if it's just a number on success, the error might still be JSON
		// Try to give a meaningful error message
		c.logger.Error("API account balance fetch failed", zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		return 0, fmt.Errorf("API account balance fetch failed with status %d: %s", resp.StatusCode, string(body))
	}

	// --- 直接 Unmarshal 到 float64 ---
	var balance float64
	if err := json.Unmarshal(body, &balance); err != nil {
		// This error occurs if the body is not a valid JSON number (e.g., empty, string, object)
		c.logger.Error("failed to unmarshal account balance response into float64", zap.Error(err), zap.String("body", string(body)))
		return 0, fmt.Errorf("failed to unmarshal account balance response into float64: %w, body: %s", err, string(body))
	}

	return balance, nil
}
