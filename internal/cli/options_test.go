package cli

import (
	"strings"
	"testing"
)

func TestParseOptionsEventsFlag(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantEvents bool
	}{
		{"no events flag", []string{"hello"}, false},
		{"events flag", []string{"-events"}, true},
		{"events flag with prompt", []string{"-events", "hello"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseOptions(tt.args, &strings.Builder{})
			if err != nil {
				t.Fatalf("parseOptions: %v", err)
			}
			if opts.events != tt.wantEvents {
				t.Fatalf("events = %v, want %v", opts.events, tt.wantEvents)
			}
		})
	}
}