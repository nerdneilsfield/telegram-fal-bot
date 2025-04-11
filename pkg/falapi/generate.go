package falapi

import (
	"context" // Add context for polling timeout
	"encoding/json"
	"fmt"
	"io"
	"net/http" // Ensure net/http is imported
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

// SubmitGenerationRequest submits the task and returns the request ID.
func (c *Client) SubmitGenerationRequest(prompt string, loraWeights []LoraWeight, loraNames []string, imageSize string, numInferenceSteps int, guidanceScale float64) (string, error) {
	c.logger.Debug("Submitting generation request", zap.String("prompt", prompt), zap.Strings("loraNames", loraNames))
	// Construct the payload using the full schema
	payload := GenerateRequest{
		Prompt: prompt,
		// Assuming loraID is the 'path' for a LoraWeight object
		Loras:     loraWeights,
		NumImages: 1, // Default or make configurable
		// Set other parameters as needed from config or defaults
		ImageSize:           imageSize, // Example default
		NumInferenceSteps:   numInferenceSteps,
		GuidanceScale:       guidanceScale,
		EnableSafetyChecker: false,  // Explicitly set safety checker to false
		OutputFormat:        "jpeg", // Explicitly set output format to jpeg
	}
	// Adjust the URL to the base queue endpoint
	// Example: c.generateURL = "https://queue.fal.run/fal-ai/flux-lora"
	respBody, err := c.doPostRequest(c.generateURL, payload)
	if err != nil {
		// Check if the error is due to status code >= 400, which might contain SubmitResponse format
		// Try to unmarshal into SubmitResponse even on error for potential request_id
		var submitResp SubmitResponse
		if json.Unmarshal(respBody, &submitResp) == nil && submitResp.RequestID != "" {
			// If we got a request ID despite the HTTP error, maybe the submission partially worked?
			// Or maybe the error response *is* the SubmitResponse format.
			// Log this unusual case but return the ID if present.
			c.logger.Warn("Warning: Received HTTP error but parsed request_id", zap.String("request_id", submitResp.RequestID), zap.Error(err))
			return submitResp.RequestID, nil // Return ID but maybe log the original error
		}
		// If unmarshal fails or no ID, return original error
		return "", fmt.Errorf("generation submission failed: %w", err)
	}

	var response SubmitResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal submission response: %w, body: %s", err, string(respBody))
	}

	if response.RequestID == "" {
		return "", fmt.Errorf("request_id not found in submission response: %s", string(respBody))
	}

	return response.RequestID, nil
}

// GetRequestStatus polls the status endpoint.
func (c *Client) GetRequestStatus(requestID, modelEndpoint string) (*StatusResponse, error) {
	// Construct the status URL: modelEndpoint + "/requests/" + requestID + "/status"
	// Ensure modelEndpoint doesn't have trailing slash
	statusURL := fmt.Sprintf("%s/requests/%s/status", strings.TrimSuffix(modelEndpoint, "/"), requestID)

	req, err := http.NewRequest("GET", statusURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create status request: %w", err)
	}
	req.Header.Set("Authorization", "Key "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send status request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read status response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Try to parse error response as StatusResponse for potential details
		var statusResp StatusResponse
		if json.Unmarshal(body, &statusResp) == nil && statusResp.Error != nil {
			return &statusResp, fmt.Errorf("API status check failed with status %d: %s", resp.StatusCode, statusResp.Error.Message)
		}
		return nil, fmt.Errorf("API status check failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response StatusResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal status response: %w, body: %s", err, string(body))
	}
	return &response, nil
}

// GetGenerationResult fetches the final result.
func (c *Client) GetGenerationResult(requestID, modelEndpoint string) (*GenerateResponse, error) {
	// Construct the result URL: modelEndpoint + "/requests/" + requestID
	resultURL := fmt.Sprintf("%s/requests/%s", strings.TrimSuffix(modelEndpoint, "/"), requestID)

	req, err := http.NewRequest("GET", resultURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create result request: %w", err)
	}
	req.Header.Set("Authorization", "Key "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send result request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read result response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		// Attempt to parse potential error details from GenerateResponse structure if API uses it
		// Or just return the generic error
		return nil, fmt.Errorf("API result fetch failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response GenerateResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal generation result: %w, body: %s", err, string(body))
	}

	// Optional: Check within the response if there's an explicit error field even with 200 OK
	// if response.Error != nil { ... }

	return &response, nil
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
