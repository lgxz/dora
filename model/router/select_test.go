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
			Name: "alpha", BaseURL: "https://alpha.example", API: "chat_completions", APIKey: "test-key",
			Profiles: []registry.Profile{
				{Name: "a-text", Capabilities: []dora.Capability{dora.CapabilityText}},
				{Name: "a-vision", Capabilities: []dora.Capability{dora.CapabilityText, dora.CapabilityImageInput}},
			},
		},
		{
			Name: "beta", BaseURL: "https://beta.example", API: "chat_completions", APIKey: "test-key",
			Profiles: []registry.Profile{
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
	if sel.provider.Name != "alpha" || sel.profile.Name != "a-vision" {
		t.Fatalf("selection = %s/%s", sel.provider.Name, sel.profile.Name)
	}
}

func TestSelectEmptyConstraintsSelectsFirst(t *testing.T) {
	sel, err := Select(testCatalog(), dora.Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name != "alpha" || sel.profile.Name != "a-text" {
		t.Fatalf("selection = %s/%s", sel.provider.Name, sel.profile.Name)
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
	if sel.provider.Name != "beta" || sel.profile.Name != "b-vision" {
		t.Fatalf("selection = %s/%s", sel.provider.Name, sel.profile.Name)
	}
}

func TestSelectTextIsExplicit(t *testing.T) {
	// A model that does not advertise "text" is skipped by Needs:[text].
	cat, err := registry.NewCatalog(registry.Config{Providers: []registry.ProviderConfig{{
		Name: "p", BaseURL: "https://p.example", API: "chat_completions", APIKey: "test-key",
		Profiles: []registry.Profile{
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
	if sel.profile.Name != "text" {
		t.Fatalf("selection = %s", sel.profile.Name)
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
	if sel.profile.Name != "a-vision" {
		t.Fatalf("profile = %q", sel.profile.Name)
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
	if sel.provider.Name != "beta" || sel.profile.Name != "b-vision" {
		t.Fatalf("selection = %s/%s", sel.provider.Name, sel.profile.Name)
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

func TestSelectSkipsProviderWithoutAPIKey(t *testing.T) {
	// Provider A has no key and appears first with a matching model; provider B
	// has a key. Selection must skip A and pick B.
	cat, err := registry.NewCatalog(registry.Config{Providers: []registry.ProviderConfig{
		{
			Name: "A", BaseURL: "https://a.example", API: "chat_completions",
			Profiles: []registry.Profile{
				{Name: "a-text", Capabilities: []dora.Capability{dora.CapabilityText}},
			},
		},
		{
			Name: "B", BaseURL: "https://b.example", API: "chat_completions", APIKey: "test-key",
			Profiles: []registry.Profile{
				{Name: "b-text", Capabilities: []dora.Capability{dora.CapabilityText}},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sel, err := Select(cat, dora.Constraints{Needs: []dora.Capability{dora.CapabilityText}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.provider.Name != "B" || sel.profile.Name != "b-text" {
		t.Fatalf("selection = %s/%s, want B/b-text", sel.provider.Name, sel.profile.Name)
	}
}

func TestSelectAllProvidersWithoutAPIKey(t *testing.T) {
	cat, err := registry.NewCatalog(registry.Config{Providers: []registry.ProviderConfig{
		{
			Name: "A", BaseURL: "https://a.example", API: "chat_completions",
			Profiles: []registry.Profile{
				{Name: "a-text", Capabilities: []dora.Capability{dora.CapabilityText}},
			},
		},
		{
			Name: "B", BaseURL: "https://b.example", API: "chat_completions",
			Profiles: []registry.Profile{
				{Name: "b-text", Capabilities: []dora.Capability{dora.CapabilityText}},
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(cat, dora.Constraints{Needs: []dora.Capability{dora.CapabilityText}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
