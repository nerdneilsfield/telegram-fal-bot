package bot

import (
	"sync"
	"time"
)

// UserState definition moved to types.go
/*
type UserState struct {
	UserID            int64
	ChatID            int64 // Added to store chat ID
	MessageID         int   // Added to store the message ID of the status/keyboard message
	Action            string // e.g., "awaiting_prompt", "awaiting_lora_selection", "awaiting_base_lora_selection", "awaiting_config_value"
	OriginalCaption   string
	SelectedLoras     []string // Stores NAMES of selected STANDARD LoRAs
	SelectedBaseLoras []string // Stores NAMES of the selected Base LoRAs (or empty)
	LastUpdated       time.Time
	// For config updates
	ConfigFieldToUpdate string
	ImageFileURL      string // Store image URL if interaction started with photo
}
*/

// StateManager manages user states concurrently and handles expiration.
type StateManager struct {
	states map[int64]*UserState // Use UserState type defined in types.go
	mu     sync.RWMutex
}

// NewStateManager creates a new StateManager.
func NewStateManager() *StateManager {
	return &StateManager{
		states: make(map[int64]*UserState),
	}
}

// SetState stores or updates a user's state.
func (sm *StateManager) SetState(userID int64, state *UserState) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	state.LastUpdated = time.Now()
	sm.states[userID] = state
}

// GetState retrieves a user's state.
func (sm *StateManager) GetState(userID int64) (*UserState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	state, ok := sm.states[userID]
	if !ok {
		return nil, false
	}
	// Optional: Check for expiration here if needed
	// if time.Since(state.LastUpdated) > StateTimeout { ... }
	return state, true
}

// ClearState removes a user's state.
func (sm *StateManager) ClearState(userID int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.states, userID)
}

// GetAction retrieves the current action for a user.
func (sm *StateManager) GetAction(userID int64) (string, bool) {
	state, ok := sm.GetState(userID)
	if !ok {
		return "", false
	}
	return state.Action, true
}

// ToggleLoraSelection (Keep this method, it works on state.SelectedLoras)
// It should operate on the standard LoRA selection.
func (sm *StateManager) ToggleLoraSelection(userID int64, loraID string) (selected []string, ok bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	state, exists := sm.states[userID]
	if !exists || (state.Action != "awaiting_lora_selection" && state.Action != "awaiting_base_lora_selection") { // Allow toggling in both selection phases for flexibility? Or restrict base lora toggle later?
		// Let's restrict for now: only allow standard lora toggling during 'awaiting_lora_selection'
		// The base lora selection will be handled separately
		// if !exists || state.Action != "awaiting_lora_selection" {
		// 	 return nil, false
		// }
		// Re-evaluating: Callback uses Lora ID. Need a way to map ID back to Name to store in state.SelectedLoras
		// This method seems complex if it needs BotDeps to find Lora by ID.
		// It's simpler to handle the toggle logic directly in HandleCallbackQuery where BotDeps is available.
		// Commenting out this method for now.
		/*
			// 检查 LoRA 是否已选择
			foundIndex := -1
			for i, id := range state.SelectedLoras { // This assumes SelectedLoras stores IDs, but it should store Names
				if id == loraID {
					foundIndex = i
					break
				}
			}
			if foundIndex != -1 { // 已选择，移除
				state.SelectedLoras = append(state.SelectedLoras[:foundIndex], state.SelectedLoras[foundIndex+1:]...)
			} else { // 未选择，添加
				state.SelectedLoras = append(state.SelectedLoras, loraID) // Appending ID, should be Name
			}
			state.LastUpdated = time.Now()
			return state.SelectedLoras, true
		*/
		return nil, false // Mark as unused for now
	}
	// Keep the logic here if needed, but ensure it uses names and handles ID->Name lookup correctly
	return nil, false // Placeholder

}
