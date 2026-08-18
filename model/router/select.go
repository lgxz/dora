// Package router implements constraint-based model selection and a cached
// dora.Model that routes requests to the selected backend.
package router

import (
	"errors"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/registry"
)

// ErrNotFound is returned when no catalog entry satisfies the constraints.
var ErrNotFound = errors.New("router: no model satisfies the constraints")

// selection is the internal resolved choice. It is never exported.
type selection struct {
	provider registry.ProviderConfig
	model    registry.ModelConfig
}

// Select returns the first catalog entry, in provider then model order, that
// satisfies every non-empty field of the constraints. Selection is silent: no
// ambiguity error, and the first match wins.
func Select(cat *registry.Catalog, c dora.Constraints) (selection, error) {
	for _, provider := range cat.Providers() {
		if c.Provider != "" && provider.Name != c.Provider {
			continue
		}
		for _, model := range provider.Models {
			if c.Profile != "" && model.Name != c.Profile {
				continue
			}
			if !satisfies(model.Capabilities, c.Needs) {
				continue
			}
			return selection{provider: provider, model: model}, nil
		}
	}
	return selection{}, ErrNotFound
}

// satisfies reports whether capabilities contains every requested need.
func satisfies(capabilities []dora.Capability, needs []dora.Capability) bool {
	for _, need := range needs {
		found := false
		for _, have := range capabilities {
			if have == need {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
