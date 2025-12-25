package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	i18n "github.com/nerdneilsfield/telegram-fal-bot/internal/i18n"
	st "github.com/nerdneilsfield/telegram-fal-bot/internal/storage"
	falapi "github.com/nerdneilsfield/telegram-fal-bot/pkg/falapi"
	"go.uber.org/zap"
)

// GenerationParameters holds the final parameters for a generation request.
// Consolidates user config, defaults, and state.
type GenerationParameters struct {
	Prompt            string
	ImageSize         string
	NumInferenceSteps int
	GuidanceScale     float64
	NumImages         int
}

// prepareGenerationParameters fetches user config and merges with defaults and state.
func prepareGenerationParameters(userID int64, userState *UserState, deps BotDeps) (*GenerationParameters, error) {
	userCfg, err := st.GetUserGenerationConfig(deps.DB, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		deps.Logger.Error("Failed to get user config before generation", zap.Error(err), zap.Int64("user_id", userID))
		// Continue with defaults, but log the error
	}

	defaultCfg := deps.Config.DefaultGenerationSettings
	params := &GenerationParameters{
		Prompt:            userState.OriginalCaption,
		ImageSize:         defaultCfg.ImageSize,
		NumInferenceSteps: defaultCfg.NumInferenceSteps,
		GuidanceScale:     defaultCfg.GuidanceScale,
		NumImages:         defaultCfg.NumImages,
	}

	if userCfg != nil {
		params.ImageSize = userCfg.ImageSize
		params.NumInferenceSteps = userCfg.NumInferenceSteps
		params.GuidanceScale = userCfg.GuidanceScale
		params.NumImages = userCfg.NumImages
	}

	return params, nil
}

// RequestInfo holds details for a single LoRA combination request.
type RequestInfo struct {
	StandardLora LoraConfig
	BaseLoras    []LoraConfig
	Params       *GenerationParameters
}

// validateAndPrepareRequests checks LoRAs, balance, and prepares individual requests.
// Returns a slice of valid RequestInfo, a slice of initial error messages, and the total number of valid requests.
func validateAndPrepareRequests(userID int64, userState *UserState, params *GenerationParameters, deps BotDeps) ([]RequestInfo, []string, int) {
	var validRequests []RequestInfo
	var initialErrors []string
	userLang := getUserLanguagePreference(userID, deps)

	if len(userState.SelectedLoras) == 0 {
		deps.Logger.Error("validateAndPrepareRequests called with no selected standard LoRAs", zap.Int64("userID", userID))
		initialErrors = append(initialErrors, deps.I18n.T(userLang, "generate_error_no_standard_lora"))
		return nil, initialErrors, 0
	}

	// Find the selected Base LoRAs (if any)
	selectedBaseLoras := []LoraConfig{}
	for _, name := range userState.SelectedBaseLoras {
		detail, found := findLoraByName(name, deps.BaseLoRA)
		if !found {
			deps.Logger.Error("Selected Base LoRA name not found in config, proceeding without it", zap.String("name", name), zap.Int64("userID", userID))
			continue
		}
		deps.Logger.Info("Found selected Base LoRA", zap.String("name", detail.Name), zap.Int64("userID", userID))
		selectedBaseLoras = append(selectedBaseLoras, detail)
	}

	numRequests := 0
	standardLoraDetailsMap := make(map[string]LoraConfig)

	// Validate standard LoRAs
	for _, name := range userState.SelectedLoras {
		detail, found := findLoraByName(name, deps.LoRA)
		if found {
			standardLoraDetailsMap[name] = detail
			numRequests++
		} else {
			deps.Logger.Error("Selected standard LoRA name not found in config during preparation", zap.String("name", name), zap.Int64("userID", userID))
			initialErrors = append(initialErrors, deps.I18n.T(userLang, "generate_error_find_lora", "name", name))
		}
	}

	// Balance Check (adjusted for valid requests)
	if deps.BalanceManager != nil && numRequests > 0 {
		totalCost := deps.BalanceManager.GetCost() * float64(numRequests)
		currentBal := deps.BalanceManager.GetBalance(userID)
		if currentBal < totalCost {
			formattedCost := fmt.Sprintf("%.2f", totalCost)
			formattedCurrent := fmt.Sprintf("%.2f", currentBal)
			errMsg := deps.I18n.T(userLang, "generate_error_insufficient_balance_multi",
				"cost", formattedCost,
				"count", numRequests,
				"current", formattedCurrent,
			)
			deps.Logger.Warn("Insufficient balance for multiple requests", zap.Int64("user_id", userID), zap.Int("num_requests", numRequests), zap.Float64("total_cost", totalCost), zap.Float64("current_balance", currentBal))
			initialErrors = append(initialErrors, errMsg)
			return nil, initialErrors, 0 // Return immediately if balance insufficient
		} else {
			deps.Logger.Info("User has sufficient balance for multiple requests, deduction will occur per request", zap.Int64("user_id", userID), zap.Int("num_requests", numRequests), zap.Float64("total_cost", totalCost), zap.Float64("current_balance", currentBal))
		}
	}

	// Build the list of valid RequestInfo
	for _, standardLora := range standardLoraDetailsMap {
		validRequests = append(validRequests, RequestInfo{
			StandardLora: standardLora,
			BaseLoras:    selectedBaseLoras,
			Params:       params,
		})
	}

	return validRequests, initialErrors, numRequests
}

// RequestResult holds the outcome of a single generation request.
type RequestResult struct {
	Response  *falapi.GenerateResponse
	Error     error
	ReqID     string
	LoraNames []string // LoRAs used for this specific request (Standard + Base if used)
}

func buildPrompt(basePrompt string, loras ...LoraConfig) string {
	prompt := strings.TrimSpace(basePrompt)
	if len(loras) == 0 {
		return prompt
	}

	parts := make([]string, 0, len(loras))
	for _, lora := range loras {
		appendPrompt := strings.TrimSpace(lora.AppendPrompt)
		if appendPrompt != "" {
			parts = append(parts, appendPrompt)
		}
	}
	if len(parts) == 0 {
		return prompt
	}

	prefix := strings.Join(parts, " ")
	if prompt == "" {
		return prefix
	}
	return prefix + " " + prompt
}

// executeAndPollRequest handles a single generation request lifecycle.
func executeAndPollRequest(reqInfo RequestInfo, userID int64, deps BotDeps, resultsChan chan<- RequestResult, wg *sync.WaitGroup) {
	defer wg.Done()
	userLang := getUserLanguagePreference(userID, deps)
	requestResult := RequestResult{LoraNames: []string{reqInfo.StandardLora.Name}}
	for _, baseLora := range reqInfo.BaseLoras {
		requestResult.LoraNames = append(requestResult.LoraNames, baseLora.Name)
	}

	// --- Individual Balance Deduction --- //
	if deps.BalanceManager != nil {
		canProceed, deductErr := deps.BalanceManager.CheckAndDeduct(userID)
		if !canProceed {
			var errMsg string
			if deductErr != nil {
				errMsg = deps.I18n.T(userLang, "generate_deduction_fail_error", "name", reqInfo.StandardLora.Name, "error", deductErr.Error())
			} else {
				errMsg = deps.I18n.T(userLang, "generate_deduction_fail", "name", reqInfo.StandardLora.Name)
			}
			deps.Logger.Warn("Individual balance deduction failed", zap.Int64("user_id", userID), zap.String("lora", reqInfo.StandardLora.Name), zap.Error(deductErr))
			requestResult.Error = fmt.Errorf(errMsg)
			resultsChan <- requestResult
			return
		}
		deps.Logger.Info("Balance deducted for LoRA request", zap.Int64("user_id", userID), zap.String("lora", reqInfo.StandardLora.Name))
	}

	maxLoras := deps.Config.APIEndpoints.MaxLoras
	if maxLoras <= 0 {
		maxLoras = 2
	}

	// --- Prepare LoRAs for API (Max from config) --- //
	lorasForAPI := []falapi.LoraWeight{{Path: reqInfo.StandardLora.URL, Scale: reqInfo.StandardLora.Weight}}
	addedURLs := map[string]struct{}{reqInfo.StandardLora.URL: {}}

	for _, baseLora := range reqInfo.BaseLoras {
		if len(lorasForAPI) >= maxLoras {
			deps.Logger.Debug("Skipping adding Base LoRA to API as request already has max LoRAs",
				zap.String("base_lora", baseLora.Name),
				zap.String("standard_lora", reqInfo.StandardLora.Name),
				zap.Int("max_loras", maxLoras),
			)
			continue
		}
		if _, exists := addedURLs[baseLora.URL]; !exists {
			lorasForAPI = append(lorasForAPI, falapi.LoraWeight{Path: baseLora.URL, Scale: baseLora.Weight})
			addedURLs[baseLora.URL] = struct{}{}
			deps.Logger.Debug("Adding selected Base LoRA to API request", zap.String("base_lora", baseLora.Name), zap.String("standard_lora", reqInfo.StandardLora.Name))
		} else {
			deps.Logger.Debug("Skipping adding Base LoRA to API as its URL is same as another LoRA", zap.String("base_lora", baseLora.Name), zap.String("standard_lora", reqInfo.StandardLora.Name))
		}
	}

	promptLoras := append([]LoraConfig{}, reqInfo.BaseLoras...)
	promptLoras = append(promptLoras, reqInfo.StandardLora)
	prompt := buildPrompt(reqInfo.Params.Prompt, promptLoras...)

	// --- Submit Single Request --- //
	deps.Logger.Debug("Submitting request for LoRA combo",
		zap.Strings("names", requestResult.LoraNames),
		zap.Int("api_lora_count", len(lorasForAPI)),
		zap.Float64("guidance_scale", reqInfo.Params.GuidanceScale),
	)
	requestID, err := deps.FalClient.SubmitGenerationRequest(
		prompt,
		lorasForAPI,
		requestResult.LoraNames,
		reqInfo.Params.ImageSize,
		reqInfo.Params.NumInferenceSteps,
		reqInfo.Params.GuidanceScale,
		reqInfo.Params.NumImages,
	)
	if err != nil {
		errMsg := deps.I18n.T(userLang, "generate_submit_fail", "loras", strings.Join(requestResult.LoraNames, "+"), "error", err.Error())
		deps.Logger.Error("SubmitGenerationRequest failed", zap.Error(err), zap.Int64("user_id", userID), zap.Strings("loras", requestResult.LoraNames))
		requestResult.Error = fmt.Errorf(errMsg)
		if deps.BalanceManager != nil {
			deps.Logger.Warn("Submission failed after deduction, no refund method.", zap.Int64("user_id", userID), zap.Strings("loras", requestResult.LoraNames), zap.Float64("amount", deps.BalanceManager.GetCost()))
		}
		resultsChan <- requestResult
		return
	}
	requestResult.ReqID = requestID
	deps.Logger.Info("Submitted individual task", zap.Int64("user_id", userID), zap.String("request_id", requestID), zap.Strings("loras", requestResult.LoraNames))

	// --- Poll For Result --- //
	pollInterval := 5 * time.Second
	generationTimeout := 5 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), generationTimeout)
	defer cancel()

	result, err := deps.FalClient.PollForResult(ctx, requestID, deps.Config.APIEndpoints.FluxLora, pollInterval)
	if err != nil {
		errMsg := formatPollError(err, requestResult.LoraNames, requestID, userLang, deps.I18n)
		deps.Logger.Error("PollForResult failed", zap.Error(err), zap.Int64("user_id", userID), zap.String("request_id", requestID), zap.Strings("loras", requestResult.LoraNames))
		requestResult.Error = fmt.Errorf(errMsg)
		resultsChan <- requestResult
		return
	}

	deps.Logger.Info("Successfully polled result", zap.String("request_id", requestID), zap.Strings("loras", requestResult.LoraNames))
	requestResult.Response = result
	resultsChan <- requestResult
}

// formatPollError translates polling errors into user-friendly messages using i18n.
func formatPollError(err error, loraNames []string, requestID string, userLang *string, i18nManager *i18n.Manager) string {
	rawErrMsg := err.Error()
	loraNamesStr := strings.Join(loraNames, "+")
	truncatedID := truncateID(requestID)

	if errors.Is(err, context.DeadlineExceeded) {
		return i18nManager.T(userLang, "generate_poll_timeout", "loras", loraNamesStr, "reqID", truncatedID)
	} else if strings.Contains(rawErrMsg, "API status check failed with status 422") || strings.Contains(rawErrMsg, "API result fetch failed with status 422") {
		detailMsg := ""
		if idx := strings.Index(rawErrMsg, "{\"detail\":"); idx != -1 {
			var detail struct {
				Detail []struct {
					Msg string `json:"msg"`
				} `json:"detail"`
			}
			if json.Unmarshal([]byte(rawErrMsg[idx:]), &detail) == nil && len(detail.Detail) > 0 {
				detailMsg = detail.Detail[0].Msg
			}
		}
		if detailMsg != "" {
			return i18nManager.T(userLang, "generate_poll_error_422_detail", "loras", loraNamesStr, "detail", detailMsg)
		} else {
			return i18nManager.T(userLang, "generate_poll_error_422", "loras", loraNamesStr)
		}
	} else {
		return i18nManager.T(userLang, "generate_poll_fail", "loras", loraNamesStr, "reqID", truncatedID, "error", rawErrMsg)
	}
}

// collectAndProcessResults gathers results from the channel and updates status.
func collectAndProcessResults(chatID int64, originalMessageID int, validRequestCount int, initialErrors []string, resultsChan <-chan RequestResult, deps BotDeps) ([]RequestResult, []RequestResult) {
	var successfulResults []RequestResult
	var errorsCollected []RequestResult
	numCompleted := 0
	userLang := getUserLanguagePreference(chatID, deps) // Assuming chatID can represent user preference context here

	// Prepend initial errors
	for _, errMsg := range initialErrors {
		errorsCollected = append(errorsCollected, RequestResult{Error: fmt.Errorf(errMsg)})
	}

	deps.Logger.Info("Waiting for generation results...")
	for res := range resultsChan {
		numCompleted++
		// Update status periodically - Using i18n key directly
		statusUpdate := deps.I18n.T(userLang, "generate_status_update", "completed", numCompleted, "total", validRequestCount)
		editStatus := tgbotapi.NewEditMessageText(chatID, originalMessageID, statusUpdate)
		deps.Bot.Send(editStatus)

		if res.Error != nil {
			errorsCollected = append(errorsCollected, res)
			deps.Logger.Warn("Collected error result", zap.Strings("loras", res.LoraNames), zap.String("reqID", res.ReqID), zap.Error(res.Error))
		} else if res.Response != nil {
			successfulResults = append(successfulResults, res)
			deps.Logger.Info("Collected successful result", zap.Strings("loras", res.LoraNames), zap.String("reqID", res.ReqID), zap.Int("image_count", len(res.Response.Images)))
		} else {
			deps.Logger.Error("Collected result with nil Response and nil Error", zap.Strings("loras", res.LoraNames), zap.String("reqID", res.ReqID))
			errorsCollected = append(errorsCollected, RequestResult{Error: fmt.Errorf(deps.I18n.T(userLang, "generate_result_empty", "loras", strings.Join(res.LoraNames, ",")))})
		}
	}
	return successfulResults, errorsCollected
}

// buildResultCaption constructs the final caption string based on results.
func buildResultCaption(prompt string, successfulResults []RequestResult, errorsCollected []RequestResult, duration time.Duration, userID int64, deps BotDeps) string {
	userLang := getUserLanguagePreference(userID, deps)
	captionBuilder := strings.Builder{}
	captionBuilder.WriteString(deps.I18n.T(userLang, "generate_caption_prompt", "prompt", prompt))

	if len(successfulResults) > 0 {
		var successNames []string
		for _, r := range successfulResults {
			if len(r.LoraNames) > 0 {
				successNames = append(successNames, fmt.Sprintf("`%s`", strings.Join(r.LoraNames, "+")))
			} else {
				successNames = append(successNames, deps.I18n.T(userLang, "generate_caption_success_unknown"))
			}
		}
		captionBuilder.WriteString(deps.I18n.T(userLang, "generate_caption_success", "count", len(successfulResults), "names", strings.Join(successNames, ", ")))
	}

	if len(errorsCollected) > 0 {
		var errorSummaries []string
		for _, e := range errorsCollected {
			if e.Error != nil {
				errorSummaries = append(errorSummaries, e.Error.Error())
			} else {
				errorSummaries = append(errorSummaries, deps.I18n.T(userLang, "generate_caption_failed_unknown"))
			}
		}
		captionBuilder.WriteString(deps.I18n.T(userLang, "generate_caption_failed", "count", len(errorsCollected), "summaries", strings.Join(errorSummaries, ", ")))
	}

	captionBuilder.WriteString(deps.I18n.T(userLang, "generate_caption_duration", "duration", fmt.Sprintf("%.1f", duration.Seconds())))
	if deps.BalanceManager != nil {
		finalBalance := deps.BalanceManager.GetBalance(userID)
		captionBuilder.WriteString(deps.I18n.T(userLang, "generate_caption_balance", "balance", fmt.Sprintf("%.2f", finalBalance)))
	}
	return captionBuilder.String()
}

// sendResultsToUser sends the generated images and caption via Telegram.
// It handles single image and media group sending, and updates/deletes the original status message.
func sendResultsToUser(chatID int64, originalMessageID int, caption string, images []falapi.ImageInfo, deps BotDeps) error {
	var sendErr error
	userLang := getUserLanguagePreference(chatID, deps) // Assuming chatID gives user context

	if len(images) == 1 {
		// Send photo without caption first
		photoMsg := tgbotapi.NewPhoto(chatID, tgbotapi.FileURL(images[0].URL))
		if _, err := deps.Bot.Send(photoMsg); err != nil {
			deps.Logger.Error("Failed to send single photo (without caption)", zap.Error(err), zap.Int64("chat_id", chatID))
			sendErr = err // Record the first error
		} else {
			// Then send the caption as a separate message
			captionMsg := tgbotapi.NewMessage(chatID, caption)
			captionMsg.ParseMode = tgbotapi.ModeMarkdown
			if _, err := deps.Bot.Send(captionMsg); err != nil {
				deps.Logger.Error("Failed to send caption for single photo", zap.Error(err), zap.Int64("chat_id", chatID))
				if sendErr == nil { // Only record if sending photo succeeded
					sendErr = err
				}
			}
		}
	} else if len(images) > 1 {
		// Send caption first for multiple images (existing logic is fine)
		captionMsg := tgbotapi.NewMessage(chatID, caption)
		captionMsg.ParseMode = tgbotapi.ModeMarkdown
		if _, err := deps.Bot.Send(captionMsg); err != nil {
			deps.Logger.Error("Failed to send caption before media group", zap.Error(err), zap.Int64("chat_id", chatID))
			// Continue trying to send images, record the error
			sendErr = err
		}

		var mediaGroup []interface{}
		for i, img := range images {
			// Ensure media items themselves don't have captions
			photo := tgbotapi.NewInputMediaPhoto(tgbotapi.FileURL(img.URL))
			mediaGroup = append(mediaGroup, photo)
			if len(mediaGroup) == 10 || i == len(images)-1 { // Send when group reaches 10 or it's the last image
				mediaMessage := tgbotapi.NewMediaGroup(chatID, mediaGroup)
				if _, err := deps.Bot.Request(mediaMessage); err != nil {
					deps.Logger.Error("Failed to send image group chunk", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int("chunk_size", len(mediaGroup)))
					if sendErr == nil { // Record the first sending error
						sendErr = err
					}
				}
				mediaGroup = []interface{}{} // Reset for next chunk
			}
		}
	}

	// Handle original message update/deletion
	if sendErr == nil {
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, originalMessageID)
		if _, errDel := deps.Bot.Request(deleteMsg); errDel != nil {
			deps.Logger.Warn("Failed to delete original status message after sending results", zap.Error(errDel), zap.Int64("chat_id", chatID), zap.Int("message_id", originalMessageID))
		}
	} else {
		failedSendText := deps.I18n.T(userLang, "generate_warn_send_failed",
			"count", len(images),
			"error", sendErr.Error(),
			"caption", caption,
		)
		if len(failedSendText) > 4090 {
			failedSendText = failedSendText[:4090] + "..."
		}
		editErr := tgbotapi.NewEditMessageText(chatID, originalMessageID, failedSendText)
		editErr.ParseMode = tgbotapi.ModeMarkdown
		editErr.ReplyMarkup = nil
		deps.Bot.Send(editErr)
	}
	return sendErr // Return the first sending error encountered, if any
}

// handleAllFailures edits the original message to indicate complete failure.
func handleAllFailures(chatID int64, originalMessageID int, errorsCollected []RequestResult, userID int64, deps BotDeps) {
	userLang := getUserLanguagePreference(userID, deps)
	deps.Logger.Error("Generation finished with no images", zap.Int64("user_id", userID), zap.Int("failed_requests", len(errorsCollected)))
	errMsgBuilder := strings.Builder{}
	errMsgBuilder.WriteString(deps.I18n.T(userLang, "generate_error_all_failed"))

	if len(errorsCollected) > 0 {
		errMsgBuilder.WriteString(deps.I18n.T(userLang, "generate_error_all_failed_details"))
		for _, e := range errorsCollected {
			if e.Error != nil {
				errMsgBuilder.WriteString(deps.I18n.T(userLang, "generate_error_all_failed_item", "error", e.Error.Error()))
			}
		}
	}
	if deps.BalanceManager != nil {
		finalBalance := deps.BalanceManager.GetBalance(userID)
		errMsgBuilder.WriteString(deps.I18n.T(userLang, "generate_caption_balance", "balance", fmt.Sprintf("%.2f", finalBalance)))
	}
	errMsgStr := errMsgBuilder.String()

	if len(errMsgStr) > 4090 {
		errMsgStr = errMsgStr[:4090] + "..."
	}

	editErr := tgbotapi.NewEditMessageText(chatID, originalMessageID, errMsgStr)
	editErr.ParseMode = tgbotapi.ModeMarkdown
	editErr.ReplyMarkup = nil
	deps.Bot.Send(editErr)
}

// GenerateImagesForUser orchestrates the image generation process.
func GenerateImagesForUser(userState *UserState, deps BotDeps) {
	userID := userState.UserID
	chatID := userState.ChatID
	originalMessageID := userState.MessageID
	deps.StateManager.ClearState(userID) // Clear state early
	userLang := getUserLanguagePreference(userID, deps)

	if chatID == 0 || originalMessageID == 0 {
		deps.Logger.Error("GenerateImagesForUser called with invalid state", zap.Int64("userID", userID), zap.Int64("chatID", chatID), zap.Int("messageID", originalMessageID))
		deps.Bot.Send(tgbotapi.NewMessage(userID, deps.I18n.T(userLang, "generate_error_invalid_state")))
		return
	}

	// 1. Prepare Parameters
	params, err := prepareGenerationParameters(userID, userState, deps)
	if err != nil {
		// Error already logged in prepareGenerationParameters
		deps.Bot.Send(tgbotapi.NewMessage(chatID, deps.I18n.T(userLang, "error_generic")))
		return
	}

	// 2. Validate LoRAs, Check Balance, Prepare Requests
	validRequests, initialErrors, validRequestCount := validateAndPrepareRequests(userID, userState, params, deps)
	if validRequestCount == 0 {
		// Handle cases where no valid requests can be made (e.g., no LoRAs, insufficient balance)
		deps.Logger.Error("No valid generation requests could be prepared", zap.Int64("userID", userID), zap.Strings("initialErrors", initialErrors))
		edit := tgbotapi.NewEditMessageText(chatID, originalMessageID, strings.Join(initialErrors, "\n"))
		edit.ReplyMarkup = nil
		deps.Bot.Send(edit)
		return
	}

	// 3. Execute Concurrent Requests
	startTime := time.Now()
	var wg sync.WaitGroup
	resultsChan := make(chan RequestResult, validRequestCount)

	deps.Logger.Info("Starting concurrent generation requests", zap.Int("count", validRequestCount), zap.Strings("selected_base_loras", userState.SelectedBaseLoras))
	statusUpdate := deps.I18n.T(userLang, "generate_submit_multi", "count", validRequestCount)
	editStatus := tgbotapi.NewEditMessageText(chatID, originalMessageID, statusUpdate)
	deps.Bot.Send(editStatus)

	for _, reqInfo := range validRequests {
		wg.Add(1)
		go executeAndPollRequest(reqInfo, userID, deps, resultsChan, &wg)
	}

	go func() {
		wg.Wait()
		close(resultsChan)
		deps.Logger.Info("All generation goroutines finished.")
	}()

	// 4. Collect and Process Results
	successfulResults, errorsCollected := collectAndProcessResults(chatID, originalMessageID, validRequestCount, initialErrors, resultsChan, deps)
	duration := time.Since(startTime)
	deps.Logger.Info("Finished collecting results", zap.Int("success_count", len(successfulResults)), zap.Int("error_count", len(errorsCollected)), zap.Duration("total_duration", duration))

	// 5. Send Final Results or Handle Failure
	allImages := []falapi.ImageInfo{}
	for _, result := range successfulResults {
		if result.Response != nil {
			allImages = append(allImages, result.Response.Images...)
		}
	}

	if len(allImages) > 0 {
		finalCaption := buildResultCaption(params.Prompt, successfulResults, errorsCollected, duration, userID, deps)
		sendResultsToUser(chatID, originalMessageID, finalCaption, allImages, deps)
	} else {
		handleAllFailures(chatID, originalMessageID, errorsCollected, userID, deps)
	}
}
