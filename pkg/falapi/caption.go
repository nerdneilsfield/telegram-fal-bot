package falapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// --- Caption Request/Response Structs ---

// CaptionSubmitRequest: Payload for submitting caption task
type CaptionSubmitRequest struct {
	ImageURL string `json:"image_url"`
	// Add other params like "prompt", "task_type" if needed for specific caption modes
}

// CaptionSubmitResponse: Response after submitting caption task
// (Can likely reuse the SubmitResponse struct if identical)
// type CaptionSubmitResponse struct {
//  RequestID string `json:"request_id"`
//  Status    string `json:"status"`
// }

// CaptionResultResponse: Final result for captioning
type CaptionResultResponse struct {
	Results string `json:"results"` // The caption text
	// Include other fields if the API returns more (e.g., timings, logs)
}

// --- Caption API Call Functions ---

// SubmitCaptionRequest submits the caption task and returns the request ID.
func (c *Client) SubmitCaptionRequest(imageURL string) (string, error) {
	payload := CaptionSubmitRequest{
		ImageURL: imageURL,
	}
	// c.captionURL should be like "https://queue.fal.run/fal-ai/florence-2-large/more-detailed-caption"
	respBody, err := c.doPostRequest(c.captionURL, payload)
	if err != nil {
		// Try parsing SubmitResponse even on error
		var submitResp SubmitResponse
		if json.Unmarshal(respBody, &submitResp) == nil && submitResp.RequestID != "" {
			fmt.Printf("Warning: Received HTTP error during caption submit but parsed request_id: %s. Error: %v\n", submitResp.RequestID, err)
			return submitResp.RequestID, nil
		}
		return "", fmt.Errorf("caption submission failed: %w", err)
	}

	var response SubmitResponse // Reuse SubmitResponse struct
	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal caption submission response: %w, body: %s", err, string(respBody))
	}

	if response.RequestID == "" {
		return "", fmt.Errorf("request_id not found in caption submission response: %s", string(respBody))
	}

	return response.RequestID, nil
}

// GetCaptionResult fetches the final caption result.
func (c *Client) GetCaptionResult(requestID, captionEndpoint string) (string, error) {
	// Construct the result URL using url.JoinPath for correctness
	resultURL, err := url.JoinPath(c.baseURL, captionEndpoint, "requests", requestID)
	if err != nil {
		return "", fmt.Errorf("failed to construct caption result URL: %w", err)
	}

	req, err := http.NewRequest("GET", resultURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create caption result request: %w", err)
	}
	req.Header.Set("Authorization", "Key "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send caption result request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read caption result response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("API caption result fetch failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response CaptionResultResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal caption result: %w, body: %s", err, string(body))
	}

	if response.Results == "" {
		// Handle cases where result might be empty string legitimately vs. missing field
		fmt.Printf("Warning: Caption result string is empty for request %s. Body: %s\n", requestID, string(body))
		// Decide if this is an error or just an empty caption
	}

	return response.Results, nil
}

// PollForCaptionResult polls status and fetches the caption string when completed.
func (c *Client) PollForCaptionResult(ctx context.Context, requestID, captionEndpoint string, pollInterval time.Duration) (string, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Use the same modelEndpoint logic as PollForResult, just point to captionEndpoint
	statusCheckEndpoint := strings.Replace(captionEndpoint, "/more-detailed-caption", "", 1) // Base endpoint for status checks

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("polling timed out for caption request %s: %w", requestID, ctx.Err())
		case <-ticker.C:
			// Status endpoint is usually the base model endpoint + /requests/.../status
			statusResp, err := c.GetRequestStatus(requestID, statusCheckEndpoint) // Use the shared GetRequestStatus
			if err != nil {
				return "", fmt.Errorf("error polling caption status for %s: %w", requestID, err)
			}

			fmt.Printf("Polling caption status for %s: %s\n", requestID, statusResp.Status) // Debug log

			switch statusResp.Status {
			case "COMPLETED":
				// Fetch the final caption result
				return c.GetCaptionResult(requestID, statusCheckEndpoint) // Use base endpoint for result fetch too
			case "FAILED":
				errMsg := "captioning failed"
				if statusResp.Error != nil {
					errMsg = fmt.Sprintf("captioning failed: %s", statusResp.Error.Message)
				}
				return "", fmt.Errorf(errMsg+" (request_id: %s)", requestID)
			case "IN_PROGRESS", "IN_QUEUE":
				continue // Keep polling
			default:
				return "", fmt.Errorf("unknown caption status '%s' for request %s", statusResp.Status, requestID)
			}
		}
	}
}
