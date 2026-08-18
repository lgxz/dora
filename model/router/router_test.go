package router

import (
	"context"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/registry"
)

// stubModel records construction and returns a fixed response.
type stubModel struct {
	content string
}

func (s *stubModel) Generate(context.Context, dora.Request) (dora.Response, error) {
	return dora.Response{Content: s.content}, nil
}

func (s *stubModel) GenerateStream(ctx context.Context, req dora.Request, emit func(dora.ModelEvent)) (dora.Response, error) {
	if emit != nil {
		emit(dora.ModelEvent{Kind: dora.ModelEventContentDelta, Delta: s.content})
	}
	return s.Generate(ctx, req)
}

func routerCatalog(t *testing.T) *registry.Catalog {
	cat, err := registry.NewCatalog(registry.Config{Providers: []registry.ProviderConfig{{
		Name: "alpha", BaseURL: "https://alpha.example", API: "chat_completions", APIKey: "test-key",
		Models: []registry.ModelConfig{
			{Name: "text", Capabilities: []dora.Capability{dora.CapabilityText}},
			{Name: "vision", Capabilities: []dora.Capability{dora.CapabilityImageInput}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestNewCachesTextModel(t *testing.T) {
	cat := routerCatalog(t)
	constructed := 0
	r, err := newRouter(cat, func(p registry.ProviderConfig, m registry.ModelConfig) (dora.Model, error) {
		constructed++
		return &stubModel{content: "hello"}, nil
	},
		dora.Constraints{Needs: []dora.Capability{dora.CapabilityText}},
		dora.Constraints{Needs: []dora.Capability{dora.CapabilityImageInput}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if constructed != 1 {
		t.Fatalf("constructed = %d, want 1", constructed)
	}
	if _, err := r.Generate(context.Background(), dora.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Generate(context.Background(), dora.Request{}); err != nil {
		t.Fatal(err)
	}
	if constructed != 1 {
		t.Fatalf("constructed = %d after generates, want 1", constructed)
	}
}

func TestGenerateStreamPassthrough(t *testing.T) {
	cat := routerCatalog(t)
	r, err := newRouter(cat, func(registry.ProviderConfig, registry.ModelConfig) (dora.Model, error) {
		return &stubModel{content: "streamed"}, nil
	},
		dora.Constraints{Needs: []dora.Capability{dora.CapabilityText}},
		dora.Constraints{Needs: []dora.Capability{dora.CapabilityImageInput}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var deltas string
	resp, err := r.GenerateStream(context.Background(), dora.Request{}, func(e dora.ModelEvent) {
		if e.Kind == dora.ModelEventContentDelta {
			deltas += e.Delta
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "streamed" || deltas != "streamed" {
		t.Fatalf("resp = %#v, deltas = %q", resp, deltas)
	}
}

func TestViewBuildsTransientModelEachCall(t *testing.T) {
	cat := routerCatalog(t)
	constructed := 0
	r, err := newRouter(cat, func(p registry.ProviderConfig, m registry.ModelConfig) (dora.Model, error) {
		constructed++
		return &stubModel{content: "a description"}, nil
	},
		dora.Constraints{Needs: []dora.Capability{dora.CapabilityText}},
		dora.Constraints{Needs: []dora.Capability{dora.CapabilityImageInput}},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Note: New already constructed the text model once.
	before := constructed
	got, err := r.View(context.Background(), dora.Image{URL: "https://example.com/a.png"}, "describe")
	if err != nil {
		t.Fatal(err)
	}
	if got != "a description" {
		t.Fatalf("view = %q", got)
	}
	if constructed != before+1 {
		t.Fatalf("constructed = %d, want %d after one View", constructed, before+1)
	}
	if _, err := r.View(context.Background(), dora.Image{URL: "https://example.com/b.png"}, "describe"); err != nil {
		t.Fatal(err)
	}
	if constructed != before+2 {
		t.Fatalf("constructed = %d, want %d after two Views", constructed, before+2)
	}
}

func TestViewPassesImageToModel(t *testing.T) {
	cat := routerCatalog(t)
	var gotImages []dora.Image
	r, err := newRouter(cat, func(p registry.ProviderConfig, m registry.ModelConfig) (dora.Model, error) {
		return &captureModel{onGenerate: func(req dora.Request) {
			if len(req.Messages) == 1 {
				gotImages = req.Messages[0].Images
			}
		}}, nil
	},
		dora.Constraints{Needs: []dora.Capability{dora.CapabilityText}},
		dora.Constraints{Needs: []dora.Capability{dora.CapabilityImageInput}},
	)
	if err != nil {
		t.Fatal(err)
	}
	img := dora.Image{URL: "https://example.com/x.png"}
	if _, err := r.View(context.Background(), img, "describe"); err != nil {
		t.Fatal(err)
	}
	if len(gotImages) != 1 || gotImages[0].URL != img.URL {
		t.Fatalf("images = %#v", gotImages)
	}
}

type captureModel struct {
	onGenerate func(dora.Request)
}

func (c *captureModel) Generate(_ context.Context, req dora.Request) (dora.Response, error) {
	if c.onGenerate != nil {
		c.onGenerate(req)
	}
	return dora.Response{Content: "ok"}, nil
}
