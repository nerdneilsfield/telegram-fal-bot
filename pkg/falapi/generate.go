package falapi

import (
	"context" // Add context for polling timeout
	"encoding/json"
	"fmt"
	"io"
	"net/http" // Ensure net/http is imported
	"net/url"  // Import net/url
	"strings"
	"time"

	"go.uber.org/zap"
)

// --- Request/Response Structs ---

// GenerateRequest: Payload for submitting the generation task
// (Based on the provided schema)
type GenerateRequest struct {
	Prompt              string       `json:"prompt"`
	ImageSize           interface{}  `json:"image_size,omitempty"` // Can be string enum or ImageSize struct
	NumInferenceSteps   int          `json:"num_inference_steps,omitempty"`
	Seed                *int         `json:"seed,omitempty"` // Pointer to allow omitting if nil
	Loras               []LoraWeight `json:"loras,omitempty"`
	GuidanceScale       float64      `json:"guidance_scale,omitempty"`
	SyncMode            bool         `json:"sync_mode,omitempty"` // Default is false (async)
	NumImages           int          `json:"num_images,omitempty"`
	EnableSafetyChecker bool         `json:"enable_safety_checker"`   // Removed omitempty for bool
	OutputFormat        string       `json:"output_format,omitempty"` // "jpeg" or "png"
	// Custom Lora ID field if 'loras' array isn't used directly
	// Lora string `json:"lora,omitempty"` // If API expects single 'lora' field instead of 'loras' array
}

// ImageSize struct for custom dimensions
type ImageSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// LoraWeight struct for the 'loras' array
type LoraWeight struct {
	Path  string  `json:"path"`            // This should be the LoRA ID/URL from config
	Scale float64 `json:"scale,omitempty"` // Default is 1.0
}

// SubmitResponse: Response received immediately after POSTing
type SubmitResponse struct {
	RequestID string `json:"request_id"`
	// Potentially other fields like initial status, queue position etc.
	Status string `json:"status"` // Example: "IN_QUEUE"
}

// StatusResponse: Response from the status endpoint
type StatusResponse struct {
	Status        string       `json:"status"`                   // e.g., "IN_QUEUE", "IN_PROGRESS", "COMPLETED", "FAILED"
	QueuePosition *int         `json:"queue_position,omitempty"` // Optional
	Logs          []LogEntry   `json:"logs,omitempty"`           // Optional logs
	Error         *ErrorDetail `json:"error,omitempty"`          // Details on failure
	// Maybe timing info even in status? Check API docs.
}
type LogEntry struct {
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
}
type ErrorDetail struct {
	// Structure depends on how Fal.ai reports errors
	Message string `json:"message"`
	// StackTrace string `json:"stacktrace,omitempty"`
}

func fallbackModelEndpoints(modelEndpoint string) []string {
	trimmed := strings.Trim(modelEndpoint, "/")
	if trimmed == "" {
		return nil
	}

	fallbacks := []string{}
	add := func(endpoint string) {
		if endpoint == "" || endpoint == trimmed {
			return
		}
		for _, existing := range fallbacks {
			if existing == endpoint {
				return
			}
		}
		fallbacks = append(fallbacks, endpoint)
	}

	if strings.HasSuffix(trimmed, "/lora") {
		withoutLora := strings.TrimSuffix(trimmed, "/lora")
		add(withoutLora)
		if strings.HasSuffix(withoutLora, "/turbo") {
			add(strings.TrimSuffix(withoutLora, "/turbo"))
		}
	}

	if strings.HasSuffix(trimmed, "/turbo") {
		add(strings.TrimSuffix(trimmed, "/turbo"))
	}

	return fallbacks
}

// GenerateResponse: Final result fetched after completion
// (This structure seems correct based on your schema)
type GenerateResponse struct {
	Images          []ImageInfo `json:"images"`
	Timings         interface{} `json:"timings,omitempty"` // Define Timings struct if needed
	Seed            uint64      `json:"seed"`              // Changed from int to uint64 to handle large seeds
	HasNsfwConcepts []bool      `json:"has_nsfw_concepts"`
	Prompt          string      `json:"prompt"`
	// May also include status info again
}

type ImageInfo struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// --- API Call Functions ---

// SubmitGenerationRequest submits a generation request to the Fal API.
// It now includes numImages as a parameter.
func (c *Client) SubmitGenerationRequest(prompt string, loras []LoraWeight, loraNames []string, imageSize string, numInferenceSteps int, guidanceScale float64, numImages int) (string, error) {
	requestURL := c.generateURL // Use the correct endpoint URL from client

	payload := map[string]interface{}{
		"prompt":                prompt,
		"loras":                 loras,
		"image_size":            imageSize,
		"num_inference_steps":   numInferenceSteps,
		"guidance_scale":        guidanceScale,
		"enable_safety_checker": false,
		"num_images":            numImages, // Include numImages in payload
	}

	// Use the helper doPostRequest for consistency
	c.logger.Debug("Submitting generation request", zap.String("request_url", requestURL))
	respBody, err := c.doPostRequest(requestURL, payload)
	if err != nil {
		// Attempt to parse SubmitResponse even on error to potentially get RequestID
		var submitResp SubmitResponse
		if json.Unmarshal(respBody, &submitResp) == nil && submitResp.RequestID != "" {
			c.logger.Warn("Warning: Received HTTP error but parsed request_id", zap.String("request_id", submitResp.RequestID), zap.Error(err))
			// Log LoRA names even if there was an error but we got an ID
			c.logger.Info("Generation request likely submitted despite error",
				zap.String("request_id", submitResp.RequestID),
				zap.Strings("lora_names_used", loraNames),
				zap.Int("num_images_requested", numImages),
			)
			return submitResp.RequestID, nil
		}
		return "", fmt.Errorf("generation submission failed: %w", err) // Return original error if no ID
	}

	var response SubmitResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal submission response: %w, body: %s", err, string(respBody))
	}

	if response.RequestID == "" {
		return "", fmt.Errorf("request_id not found in submission response: %s", string(respBody))
	}

	// Log successful submission details
	c.logger.Info("Generation request submitted successfully",
		zap.String("request_id", response.RequestID),
		zap.Strings("lora_names_used", loraNames),
		zap.Int("num_images_requested", numImages),
	)

	return response.RequestID, nil
}

// GetRequestStatus polls the status endpoint.
func (c *Client) GetRequestStatus(requestID, modelEndpoint string) (*StatusResponse, error) {
	statusResp, statusCode, err := c.getRequestStatusOnce(requestID, modelEndpoint)
	if err == nil || statusCode != http.StatusMethodNotAllowed {
		return statusResp, err
	}

	fallbacks := fallbackModelEndpoints(modelEndpoint)
	for _, fallback := range fallbacks {
		c.logger.Warn("Status endpoint returned 405, retrying with fallback endpoint",
			zap.String("model_endpoint", modelEndpoint),
			zap.String("fallback_endpoint", fallback),
			zap.String("request_id", requestID),
		)
		fallbackResp, fallbackCode, fallbackErr := c.getRequestStatusOnce(requestID, fallback)
		if fallbackErr == nil {
			return fallbackResp, nil
		}
		if fallbackCode != http.StatusMethodNotAllowed {
			return fallbackResp, fmt.Errorf("fallback status endpoint %s failed: %w", fallback, fallbackErr)
		}
	}

	return statusResp, err
}

func (c *Client) getRequestStatusOnce(requestID, modelEndpoint string) (*StatusResponse, int, error) {
	// Construct the status URL using url.JoinPath for correctness
	statusURL, err := url.JoinPath(c.baseURL, modelEndpoint, "requests", requestID, "status")
	if err != nil {
		// Although JoinPath rarely errors with valid inputs, handle it just in case
		return nil, 0, fmt.Errorf("failed to construct status URL: %w", err)
	}

	// Log the URL being requested for debugging
	c.logger.Debug("Requesting status from URL", zap.String("status_url", statusURL))

	req, err := http.NewRequest("GET", statusURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create status request: %w", err)
	}
	req.Header.Set("Authorization", "Key "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send status request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read status response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Try to parse error response as StatusResponse for potential details
		var statusResp StatusResponse
		if json.Unmarshal(body, &statusResp) == nil && statusResp.Error != nil {
			return &statusResp, resp.StatusCode, fmt.Errorf("API status check failed with status %d: %s", resp.StatusCode, statusResp.Error.Message)
		}
		return nil, resp.StatusCode, fmt.Errorf("API status check failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response StatusResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to unmarshal status response: %w, body: %s", err, string(body))
	}
	return &response, resp.StatusCode, nil
}

// GetGenerationResult fetches the final result.
func (c *Client) GetGenerationResult(requestID, modelEndpoint string) (*GenerateResponse, error) {
	resultResp, statusCode, err := c.getGenerationResultOnce(requestID, modelEndpoint)
	if err == nil || statusCode != http.StatusMethodNotAllowed {
		return resultResp, err
	}

	fallbacks := fallbackModelEndpoints(modelEndpoint)
	for _, fallback := range fallbacks {
		c.logger.Warn("Result endpoint returned 405, retrying with fallback endpoint",
			zap.String("model_endpoint", modelEndpoint),
			zap.String("fallback_endpoint", fallback),
			zap.String("request_id", requestID),
		)
		fallbackResp, fallbackCode, fallbackErr := c.getGenerationResultOnce(requestID, fallback)
		if fallbackErr == nil {
			return fallbackResp, nil
		}
		if fallbackCode != http.StatusMethodNotAllowed {
			return fallbackResp, fmt.Errorf("fallback result endpoint %s failed: %w", fallback, fallbackErr)
		}
	}

	return resultResp, err
}

func (c *Client) getGenerationResultOnce(requestID, modelEndpoint string) (*GenerateResponse, int, error) {
	// Construct the result URL using url.JoinPath for correctness
	resultURL, err := url.JoinPath(c.baseURL, modelEndpoint, "requests", requestID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to construct result URL: %w", err)
	}

	req, err := http.NewRequest("GET", resultURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create result request: %w", err)
	}
	req.Header.Set("Authorization", "Key "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send result request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read result response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Attempt to parse potential error details from GenerateResponse structure if API uses it
		// Or just return the generic error
		return nil, resp.StatusCode, fmt.Errorf("API result fetch failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response GenerateResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to unmarshal generation result: %w, body: %s", err, string(body))
	}

	// Optional: Check within the response if there's an explicit error field even with 200 OK
	// if response.Error != nil { ... }

	return &response, resp.StatusCode, nil
}

// PollForResult polls the status and fetches the result when completed.
// Includes a timeout context.
func (c *Client) PollForResult(ctx context.Context, requestID, modelEndpoint string, pollInterval time.Duration) (*GenerateResponse, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("polling timed out for request %s: %w", requestID, ctx.Err())
		case <-ticker.C:
			statusResp, err := c.GetRequestStatus(requestID, modelEndpoint)
			if err != nil {
				// Decide if the error is temporary (network) or permanent (e.g., 404 Not Found)
				// For now, return error on any status check failure during poll
				return nil, fmt.Errorf("error polling status for %s: %w", requestID, err)
			}

			c.logger.Debug("Polling status for request", zap.String("request_id", requestID), zap.String("status", statusResp.Status)) // Debug log

			switch statusResp.Status {
			case "COMPLETED":
				// Status is completed, fetch the final result
				return c.GetGenerationResult(requestID, modelEndpoint)
			case "FAILED":
				errMsg := "generation failed"
				if statusResp.Error != nil {
					errMsg = fmt.Sprintf("generation failed: %s", statusResp.Error.Message)
				} else if len(statusResp.Logs) > 0 {
					// Look for error messages in logs as fallback
					// errMsg = fmt.Sprintf("generation failed, last log: %s", statusResp.Logs[len(statusResp.Logs)-1].Message)
				}
				return nil, fmt.Errorf(errMsg+" (request_id: %s)", requestID)

			case "IN_PROGRESS", "IN_QUEUE":
				// Still working, continue polling
				continue
			default:
				// Unknown status, treat as an error
				return nil, fmt.Errorf("unknown status '%s' for request %s", statusResp.Status, requestID)
			}
		}
	}
}

// --- Caption functions would need similar Submit/Poll/Get structure ---
// func (c *Client) SubmitCaptionRequest(...) (string, error)
// func (c *Client) PollForCaptionResult(...) (string, error) // Returns caption string
