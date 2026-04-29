package cache

import (
	"context"
	"fmt"
	"strings"
)

type Cache struct {
	storage *Storage
}

func New(storage *Storage) *Cache {
	return &Cache{storage: storage}
}

func (c *Cache) IsPersistent() bool {
	return c.storage.IsPersistent()
}

func (c *Cache) Store(ctx context.Context, key string, actions []Action, variables []string, metadata map[string]any, pageURL string) (*StoreResult, error) {
	entry := NewEntry(key, actions, pageURL)
	if variables != nil {
		entry.Variables = variables
	}
	if metadata != nil {
		entry.Metadata = metadata
	}

	if err := c.storage.Write(entry); err != nil {
		return nil, fmt.Errorf("store cache entry: %w", err)
	}

	return &StoreResult{
		Success: true,
		Key:     key,
		Message: fmt.Sprintf("cached %d actions under key '%s'", len(actions), key),
	}, nil
}

func (c *Cache) Lookup(ctx context.Context, key string, currentURL string) (*LookupResult, error) {
	entry, err := c.storage.Read(key)
	if err != nil {
		return nil, fmt.Errorf("lookup cache entry: %w", err)
	}
	if entry == nil {
		return &LookupResult{Found: false}, nil
	}

	urlMatch := entry.URL == currentURL || entry.URL == "" || currentURL == ""

	return &LookupResult{
		Found:    true,
		Entry:    entry,
		URLMatch: urlMatch,
	}, nil
}

func (c *Cache) Delete(ctx context.Context, key string) error {
	return c.storage.Delete(key)
}

func (c *Cache) List(ctx context.Context) ([]string, error) {
	return c.storage.List()
}

func (c *Cache) Clear(ctx context.Context, key string) (int, error) {
	return c.storage.Clear(key)
}

func (c *Cache) InterpolateVariables(entry *Entry, variables map[string]string) []Action {
	if len(entry.Variables) == 0 || len(variables) == 0 {
		return entry.Actions
	}

	actions := make([]Action, len(entry.Actions))
	for i, action := range entry.Actions {
		actions[i] = c.interpolateAction(action, variables)
	}
	return actions
}

func (c *Cache) interpolateAction(action Action, variables map[string]string) Action {
	result := action
	result.Value = c.interpolateString(action.Value, variables)
	result.Selector = c.interpolateString(action.Selector, variables)

	if action.Fields != nil {
		result.Fields = make([]FormField, len(action.Fields))
		for i, field := range action.Fields {
			result.Fields[i] = FormField{
				Selector: c.interpolateString(field.Selector, variables),
				Value:    c.interpolateString(field.Value, variables),
			}
		}
	}

	if action.Options != nil {
		result.Options = make(map[string]any, len(action.Options))
		for k, v := range action.Options {
			if vs, ok := v.(string); ok {
				result.Options[k] = c.interpolateString(vs, variables)
			} else {
				result.Options[k] = v
			}
		}
	}

	return result
}

func (c *Cache) interpolateString(s string, variables map[string]string) string {
	if s == "" {
		return s
	}
	result := s
	for key, value := range variables {
		token := "%" + key + "%"
		result = strings.ReplaceAll(result, token, value)
	}
	return result
}

func (c *Cache) HasVariables(entry *Entry) bool {
	return entry.HasVariables()
}

func (c *Cache) ValidateVariables(entry *Entry, provided map[string]string) (missing []string) {
	for _, v := range entry.Variables {
		if _, ok := provided[v]; !ok {
			missing = append(missing, v)
		}
	}
	return missing
}

func (c *Cache) UpdateEntry(entry *Entry) error {
	return c.storage.Write(entry)
}
