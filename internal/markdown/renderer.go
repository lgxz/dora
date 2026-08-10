// Package markdown renders complete model answers for interactive terminals.
package markdown

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	charmansi "github.com/charmbracelet/x/ansi"
)

const (
	defaultWidth = 80
	minimumWidth = 20
	maximumWidth = 120
)

// Options controls terminal-specific Markdown rendering.
type Options struct {
	Width          int
	Color          bool
	DarkBackground bool
}

// Render formats one complete Markdown document for a terminal.
func Render(source string, options Options) (string, error) {
	rendererOptions := []glamour.TermRendererOption{
		glamour.WithWordWrap(normalizeWidth(options.Width)),
	}
	style := styles.DarkStyle
	if options.Color && !options.DarkBackground {
		style = styles.LightStyle
	}
	rendererOptions = append(rendererOptions, glamour.WithStandardStyle(style))
	renderer, err := glamour.NewTermRenderer(rendererOptions...)
	if err != nil {
		return "", err
	}
	rendered, err := renderer.Render(source)
	if err != nil {
		return "", err
	}
	if rendered == "" {
		return "", nil
	}
	if !options.Color {
		rendered = charmansi.Strip(rendered)
	}
	return strings.TrimRight(rendered, "\n") + "\n", nil
}

func normalizeWidth(width int) int {
	switch {
	case width <= 0:
		return defaultWidth
	case width < minimumWidth:
		return minimumWidth
	case width > maximumWidth:
		return maximumWidth
	default:
		return width
	}
}
