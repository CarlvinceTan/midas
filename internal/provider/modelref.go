package provider

import (
	"strings"

	"github.com/CarlvinceTan/midas/pkg/ai"
)

// ModelForRef resolves a stored "provider/model" reference against the catalog,
// falling back to a named stub when the catalog does not know it yet.
func ModelForRef(ref string, catalog []ai.Model) ai.Model {
	providerID, modelID := ai.SplitModelReference("", strings.TrimSpace(ref))
	for _, model := range catalog {
		if model.Provider == providerID && model.ID == modelID {
			return model
		}
	}
	return ai.Model{Provider: providerID, ID: modelID, Name: modelID}
}
