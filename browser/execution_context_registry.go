package browser

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

type executionContextRegistry struct {
	mu           sync.RWMutex
	mainWorld    map[string]map[string]int64
	utilityWorld map[string]map[string]int64
	unsubBySess  map[string]func()
}

func newExecutionContextRegistry() *executionContextRegistry {
	return &executionContextRegistry{
		mainWorld:    make(map[string]map[string]int64),
		utilityWorld: make(map[string]map[string]int64),
		unsubBySess:  make(map[string]func()),
	}
}

func (r *executionContextRegistry) AttachSession(session sessionLike) {
	if session == nil || session.ID() == "" {
		return
	}

	r.mu.Lock()
	if _, ok := r.unsubBySess[session.ID()]; ok {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	offCreated := session.On("Runtime.executionContextCreated", func(params json.RawMessage) {
		var evt struct {
			Context struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				AuxData struct {
					FrameID   string `json:"frameId"`
					IsDefault bool   `json:"isDefault"`
				} `json:"auxData"`
			} `json:"context"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		if evt.Context.AuxData.FrameID == "" {
			return
		}
		r.mu.Lock()
		switch {
		case evt.Context.AuxData.IsDefault:
			if r.mainWorld[session.ID()] == nil {
				r.mainWorld[session.ID()] = make(map[string]int64)
			}
			r.mainWorld[session.ID()][evt.Context.AuxData.FrameID] = evt.Context.ID
		case evt.Context.Name == selectorUtilityWorldName:
			if r.utilityWorld[session.ID()] == nil {
				r.utilityWorld[session.ID()] = make(map[string]int64)
			}
			r.utilityWorld[session.ID()][evt.Context.AuxData.FrameID] = evt.Context.ID
		}
		r.mu.Unlock()
	})

	offDestroyed := session.On("Runtime.executionContextDestroyed", func(params json.RawMessage) {
		var evt struct {
			ExecutionContextID int64 `json:"executionContextId"`
		}
		if json.Unmarshal(params, &evt) != nil {
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		for sessionID, byFrame := range r.mainWorld {
			for frameID, ctxID := range byFrame {
				if ctxID == evt.ExecutionContextID {
					delete(byFrame, frameID)
				}
			}
			if len(byFrame) == 0 {
				delete(r.mainWorld, sessionID)
			}
		}
		for sessionID, byFrame := range r.utilityWorld {
			for frameID, ctxID := range byFrame {
				if ctxID == evt.ExecutionContextID {
					delete(byFrame, frameID)
				}
			}
			if len(byFrame) == 0 {
				delete(r.utilityWorld, sessionID)
			}
		}
	})

	offCleared := session.On("Runtime.executionContextsCleared", func(_ json.RawMessage) {
		r.mu.Lock()
		delete(r.mainWorld, session.ID())
		delete(r.utilityWorld, session.ID())
		r.mu.Unlock()
	})

	r.mu.Lock()
	r.unsubBySess[session.ID()] = func() {
		offCreated()
		offDestroyed()
		offCleared()
	}
	r.mu.Unlock()
}

func (r *executionContextRegistry) MainWorldID(sessionID, frameID string) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if byFrame := r.mainWorld[sessionID]; byFrame != nil {
		return byFrame[frameID]
	}
	return 0
}

func (r *executionContextRegistry) UtilityWorldID(sessionID, frameID string) int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if byFrame := r.utilityWorld[sessionID]; byFrame != nil {
		return byFrame[frameID]
	}
	return 0
}

func (r *executionContextRegistry) SetUtilityWorldID(sessionID, frameID string, ctxID int64) {
	if sessionID == "" || frameID == "" || ctxID == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.utilityWorld[sessionID] == nil {
		r.utilityWorld[sessionID] = make(map[string]int64)
	}
	r.utilityWorld[sessionID][frameID] = ctxID
}

func (r *executionContextRegistry) WaitForMainWorld(ctx context.Context, session sessionLike, frameID string) (int64, error) {
	if session == nil {
		return 0, context.Canceled
	}
	if ctxID := r.MainWorldID(session.ID(), frameID); ctxID != 0 {
		return ctxID, nil
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctxID := r.MainWorldID(session.ID(), frameID); ctxID != 0 {
			return ctxID, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *executionContextRegistry) WaitForUtilityWorld(ctx context.Context, session sessionLike, frameID string) (int64, error) {
	if session == nil {
		return 0, context.Canceled
	}
	if ctxID := r.UtilityWorldID(session.ID(), frameID); ctxID != 0 {
		return ctxID, nil
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctxID := r.UtilityWorldID(session.ID(), frameID); ctxID != 0 {
			return ctxID, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *executionContextRegistry) DetachSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if off := r.unsubBySess[sessionID]; off != nil {
		off()
	}
	delete(r.unsubBySess, sessionID)
	delete(r.mainWorld, sessionID)
	delete(r.utilityWorld, sessionID)
}
