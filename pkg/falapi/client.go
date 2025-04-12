package falapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.uber.org/zap"
)

// Client holds the API key, HTTP client, logger, and base URL.
type Client struct {
	apiKey      string
	httpClient  *http.Client
	logger      *zap.Logger
	baseURL     string // Base URL for Fal API, e.g., "https://queue.fal.run"
	generateURL string // Full URL for the generation endpoint
	captionURL  string // Full URL for the caption endpoint
}

// NewClient creates a new Fal API client.
func NewClient(apiKey, baseURL, generatePath, captionPath string, logger *zap.Logger) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("Fal API key is required")
	}
	if baseURL == "" {
		return nil, errors.New("Fal API base URL is required")
	}
	if generatePath == "" {
		return nil, errors.New("Fal generate endpoint path is required")
	}
	if captionPath == "" {
		return nil, errors.New("Fal caption endpoint path is required")
	}

	// Parse the base URL to validate it
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	if parsedBaseURL.Scheme == "" || parsedBaseURL.Host == "" {
		return nil, fmt.Errorf("base URL must include scheme (e.g., https) and host")
	}
	// Use the parsed base URL string to ensure it's clean
	cleanBaseURL := parsedBaseURL.String()

	// Construct full URLs using url.JoinPath
	genURL, err := url.JoinPath(cleanBaseURL, generatePath)
	if err != nil {
		return nil, fmt.Errorf("failed to construct generate URL: %w", err)
	}
	capURL, err := url.JoinPath(cleanBaseURL, captionPath)
	if err != nil {
		return nil, fmt.Errorf("failed to construct caption URL: %w", err)
	}

	logger.Info("FalClient initialized", zap.String("baseURL", cleanBaseURL), zap.String("generateURL", genURL), zap.String("captionURL", capURL))

	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second, // Example timeout
		},
		logger:      logger.Named("FalClient"),
		baseURL:     cleanBaseURL, // Store the cleaned base URL
		generateURL: genURL,
		captionURL:  capURL,
	}, nil
}

// Helper function for making POST requests
func (c *Client) doPostRequest(url string, payload interface{}) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Log the target URL and payload size for debugging
	c.logger.Debug("Making POST request", zap.String("url", url), zap.Int("payload_size", len(jsonData)))

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Key "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Return body even on error, as it might contain useful info (like request_id)
		return body, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// SubmitGenerationRequest moved to generate.go

// GetImageCaption sends an image URL to the captioning endpoint and returns the caption.
func (c *Client) GetImageCaption(imageURL string) (string, error) {
	// ... (implementation remains here)
	payload := map[string]string{
		"image_url": imageURL,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal caption payload: %w", err)
	}

	resp, err := c.httpClient.Post(c.captionURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to send caption request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("caption request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read caption response body: %w", err)
	}

	// Assuming the response is plain text caption based on curl example
	// If it's JSON, unmarshal into a struct
	// var response struct {
	// 	 Caption string `json:"caption"`
	// }
	// if err := json.Unmarshal(body, &response); err != nil {
	// 	 return "", fmt.Errorf("failed to unmarshal caption response: %w", err)
	// }
	// return response.Caption, nil

	return string(body), nil
}
