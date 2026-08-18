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

	imageSel, err := Select(cat, image)
	if err != nil {
		return nil, err
	}
	r.imageSel = imageSel
	return r, nil
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
