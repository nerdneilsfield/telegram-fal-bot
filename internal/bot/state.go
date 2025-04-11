package bot

import (
	"sync"
	"time"
)

type UserState struct {
	UserID          int64
	Action          string   // e.g., "awaiting_lora_selection", "awaiting_caption_edit"
	OriginalCaption string   // 当等待编辑/确认 caption 时
	ImageFileURL    string   // 当处理图片时
	SelectedLoras   []string // 用户已选择的 LoRA ID
	LastUpdated     time.Time
}

type StateManager struct {
	states map[int64]*UserState
	mu     sync.RWMutex
}

func NewStateManager() *StateManager {
	return &StateManager{
		states: make(map[int64]*UserState),
	}
}

func (sm *StateManager) SetState(userID int64, state *UserState) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	state.LastUpdated = time.Now()
	sm.states[userID] = state
}

func (sm *StateManager) GetState(userID int64) (*UserState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	state, ok := sm.states[userID]
	// (可选) 清理过期的状态
	// if ok && time.Since(state.LastUpdated) > 30*time.Minute {
	//     delete(sm.states, userID) // 需要写锁，或者标记为过期
	//     return nil, false
	// }
	return state, ok
}

func (sm *StateManager) ClearState(userID int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.states, userID)
}

// 添加/移除已选择的 LoRA
func (sm *StateManager) ToggleLoraSelection(userID int64, loraID string) (selected []string, ok bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	state, exists := sm.states[userID]
	if !exists || state.Action != "awaiting_lora_selection" { // 确保在正确的状态
		return nil, false
	}

	// 检查 LoRA 是否已选择
	foundIndex := -1
	for i, id := range state.SelectedLoras {
		if id == loraID {
			foundIndex = i
			break
		}
	}

	if foundIndex != -1 { // 已选择，移除
		state.SelectedLoras = append(state.SelectedLoras[:foundIndex], state.SelectedLoras[foundIndex+1:]...)
	} else { // 未选择，添加
		state.SelectedLoras = append(state.SelectedLoras, loraID)
	}
	state.LastUpdated = time.Now()
	return state.SelectedLoras, true
}
