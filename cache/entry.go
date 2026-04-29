package cache

import "time"

const Version = 1

type ActionType string

const (
	ActionTypeClick     ActionType = "click"
	ActionTypeType      ActionType = "type"
	ActionTypeGoto      ActionType = "goto"
	ActionTypeScroll    ActionType = "scroll"
	ActionTypeWait      ActionType = "wait"
	ActionTypePress     ActionType = "press"
	ActionTypeDragDrop  ActionType = "drag_and_drop"
	ActionTypeFillForm  ActionType = "fill_form"
	ActionTypeClickHold ActionType = "click_and_hold"
	ActionTypeNavBack   ActionType = "nav_back"
)

type Action struct {
	Type      ActionType     `json:"type"`
	Selector  string         `json:"selector,omitempty"`
	Value     string         `json:"value,omitempty"`
	Button    string         `json:"button,omitempty"`
	DeltaX    float64        `json:"delta_x,omitempty"`
	DeltaY    float64        `json:"delta_y,omitempty"`
	Percent   float64        `json:"percent,omitempty"`
	Key       string         `json:"key,omitempty"`
	Duration  int            `json:"duration,omitempty"`
	Timeout   int            `json:"timeout,omitempty"`
	Steps     int            `json:"steps,omitempty"`
	Double    bool           `json:"double,omitempty"`
	Clear     bool           `json:"clear,omitempty"`
	WaitUntil string         `json:"wait_until,omitempty"`
	Fields    []FormField    `json:"fields,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

type FormField struct {
	Selector string `json:"selector"`
	Value    string `json:"value"`
}

type Entry struct {
	Version   int            `json:"version"`
	Key       string         `json:"key"`
	URL       string         `json:"url"`
	Actions   []Action       `json:"actions"`
	Variables []string       `json:"variables,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

func NewEntry(key string, actions []Action, url string) *Entry {
	return &Entry{
		Version:   Version,
		Key:       key,
		URL:       url,
		Actions:   actions,
		Timestamp: time.Now().UTC(),
	}
}

func (e *Entry) HasVariables() bool {
	return len(e.Variables) > 0
}

func (e *Entry) IsSelectorBased() bool {
	for _, action := range e.Actions {
		if action.Selector != "" {
			return true
		}
	}
	return false
}

type ActionResult struct {
	Action  Action `json:"action"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type ReplayResult struct {
	Success      bool           `json:"success"`
	Executed     int            `json:"executed"`
	FailedIndex  int            `json:"failed_index"`
	FailedAction *Action        `json:"failed_action,omitempty"`
	Error        string         `json:"error,omitempty"`
	Actions      []ActionResult `json:"actions"`
	URLWarning   string         `json:"url_warning,omitempty"`
}

type LookupResult struct {
	Found    bool   `json:"found"`
	Entry    *Entry `json:"entry,omitempty"`
	URLMatch bool   `json:"url_match"`
}

type StoreResult struct {
	Success bool   `json:"success"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

type ClearResult struct {
	Success      bool `json:"success"`
	ClearedCount int  `json:"cleared_count"`
}
