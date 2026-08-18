package router

import (
	"errors"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/registry"
)

func testCatalog() *registry.Catalog {
	cat, err := registry.NewCatalog(registry.Config{Providers: []registry.ProviderConfig{
		{
			Name: "alpha", BaseURL: "https://alpha.example", API: "chat_completions",
			Models: []registry.ModelConfig{
				{Name: "a-text", Capabilities: []dora.Capability{dora.CapabilityText}},
				{Name: "a-vision", Capabilities: []dora.Capability{dora.CapabilityText, dora.CapabilityImageInput}},
			},
		},
		{
			Name: "beta", BaseURL: "https://beta.example", API: "chat_completions",
			Models: []registry.ModelConfig{
				{Name: "b-vision", Capabilities: []dora.Capability{dora.CapabilityText, dora.CapabilityImageInput, dora.CapabilityImageOutput}},
			},
		},
	}})
	if err != nil {
		panic(err)
	}
	return cat
}

func TestSelectFirstInOrderWins(t *testing.T) {
	sel, err := Select(testCatalog(), dora.Constraints{Needs: []dora.Capability{dora.CapabilityImageInput}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name != "alpha" || sel.model.Name != "a-vision" {
		t.Fatalf("selection = %s/%s", sel.provider.Name, sel.model.Name)
	}
}

func TestSelectEmptyConstraintsSelectsFirst(t *testing.T) {
	sel, err := Select(testCatalog(), dora.Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name != "alpha" || sel.model.Name != "a-text" {
		t.Fatalf("selection = %s/%s", sel.provider.Name, sel.model.Name)
	}
}

func TestSelectNeedIntersection(t *testing.T) {
	// No entry advertises both image_input AND image_output except b-vision,
	// but b-vision lacks nothing; a-vision lacks image_output. Only b-vision
	// satisfies both.
	sel, err := Select(testCatalog(), dora.Constraints{Needs: []dora.Capability{dora.CapabilityImageInput, dora.CapabilityImageOutput}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name != "beta" || sel.model.Name != "b-vision" {
		t.Fatalf("selection = %s/%s", sel.provider.Name, sel.model.Name)
	}
}

func TestSelectTextIsExplicit(t *testing.T) {
	// A model that does not advertise "text" is skipped by Needs:[text].
	cat, err := registry.NewCatalog(registry.Config{Providers: []registry.ProviderConfig{{
		Name: "p", BaseURL: "https://p.example", API: "chat_completions",
		Models: []registry.ModelConfig{
			{Name: "vision-only", Capabilities: []dora.Capability{dora.CapabilityImageInput}},
			{Name: "text", Capabilities: []dora.Capability{dora.CapabilityText}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	sel, err := Select(cat, dora.Constraints{Needs: []dora.Capability{dora.CapabilityText}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.model.Name != "text" {
		t.Fatalf("selection = %s", sel.model.Name)
	}
}

func TestSelectProviderScoping(t *testing.T) {
	sel, err := Select(testCatalog(), dora.Constraints{Provider: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name != "beta" {
		t.Fatalf("provider = %q", sel.provider.Name)
	}
}

func TestSelectProfileScoping(t *testing.T) {
	sel, err := Select(testCatalog(), dora.Constraints{Profile: "a-vision"})
	if err != nil {
		t.Fatal(err)
	}
	if sel.model.Name != "a-vision" {
		t.Fatalf("profile = %q", sel.model.Name)
	}
}

func TestSelectCombinedConstraints(t *testing.T) {
	sel, err := Select(testCatalog(), dora.Constraints{
		Provider: "beta",
		Profile:  "b-vision",
		Needs:    []dora.Capability{dora.CapabilityImageInput},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name != "beta" || sel.model.Name != "b-vision" {
		t.Fatalf("selection = %s/%s", sel.provider.Name, sel.model.Name)
	}
}

func TestSelectProviderMiss(t *testing.T) {
	if _, err := Select(testCatalog(), dora.Constraints{Provider: "gamma"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestSelectProfileMiss(t *testing.T) {
	if _, err := Select(testCatalog(), dora.Constraints{Profile: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestSelectReservedCapabilityMatchesNothing(t *testing.T) {
	if _, err := Select(testCatalog(), dora.Constraints{Needs: []dora.Capability{dora.CapabilityAudioInput}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
