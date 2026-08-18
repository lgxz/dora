package router

import (
	"context"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/registry"
)

// constructFunc is the seam used to instantiate a dora.Model from a resolved
// profile. Tests inject a stub; production uses registry.Construct.
type constructFunc func(registry.ProviderConfig, registry.ModelConfig) (dora.Model, error)

// Router implements dora.Model and routes to a cached text model plus a
// transiently-constructed visual model for image viewing.
type Router struct {
	catalog   *registry.Catalog
	construct constructFunc

	textModel dora.Model
	textSel   selection

	imageSet bool
	imageSel selection
}

// New resolves and caches the text and image selections from the catalog, then
// constructs (and caches) the text model. The image model is constructed
// transiently on each View call.
func New(cat *registry.Catalog, text, image dora.Constraints) (*Router, error) {
	return newRouter(cat, registry.Construct, text, image)
}

func newRouter(cat *registry.Catalog, construct constructFunc, text, image dora.Constraints) (*Router, error) {
	r := &Router{catalog: cat, construct: construct}
	textSel, err := Select(cat, text)
	if err != nil {
		return nil, err
	}
	textModel, err := construct(textSel.provider, textSel.model)
	if err != nil {
		return nil, err
	}
	r.textSel = textSel
	r.textModel = textModel

	// The image selection is resolved lazily: it is cached if found, but a miss
	// is not fatal at construction time (a provider may not advertise
	// image_input). View then reports the miss.
	if imageSel, err := Select(cat, image); err == nil {
		r.imageSel = imageSel
		r.imageSet = true
	}
	return r, nil
}

// SetThinking overrides the cached text model's thinking mode (for --thinking)
// and reconstructs the cached text model from the same catalog entry.
func (r *Router) SetThinking(thinking *string) error {
	r.textSel.model.Thinking = thinking
	model, err := r.construct(r.textSel.provider, r.textSel.model)
	if err != nil {
		return err
	}
	r.textModel = model
	return nil
}

// Generate delegates to the cached text model.
func (r *Router) Generate(ctx context.Context, req dora.Request) (dora.Response, error) {
	return r.textModel.Generate(ctx, req)
}

// GenerateStream delegates to the cached text model when it supports streaming;
// otherwise it falls back to Generate.
func (r *Router) GenerateStream(ctx context.Context, req dora.Request, emit func(dora.ModelEvent)) (dora.Response, error) {
	if streamer, ok := r.textModel.(dora.StreamingModel); ok {
		return streamer.GenerateStream(ctx, req, emit)
	}
	return r.textModel.Generate(ctx, req)
}

// View constructs a transient visual model from the cached image selection,
// sends it a single image-bearing message, and returns the text description.
func (r *Router) View(ctx context.Context, image dora.Image, prompt string) (string, error) {
	if !r.imageSet {
		return "", ErrNotFound
	}
	model, err := r.construct(r.imageSel.provider, r.imageSel.model)
	if err != nil {
		return "", err
	}
	req := dora.Request{
		Messages: []dora.Message{{
			Role:    dora.RoleUser,
			Content: prompt,
			Images:  []dora.Image{image},
		}},
	}
	resp, err := model.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
