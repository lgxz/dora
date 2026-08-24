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

func TestParseOptionsWorkdir(t *testing.T) {
	opts, err := parseOptions([]string{"--workdir", "project", "hello"}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.workdir != "project" || len(opts.promptArgs) != 1 || opts.promptArgs[0] != "hello" {
		t.Fatalf("options = %#v", opts)
	}
}

func TestParseOptionsColor(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      string
		wantError bool
	}{
		{name: "default", args: []string{"hello"}, want: "auto"},
		{name: "auto", args: []string{"--color=auto", "hello"}, want: "auto"},
		{name: "always", args: []string{"--color=always", "hello"}, want: "always"},
		{name: "never", args: []string{"--color=never", "hello"}, want: "never"},
		{name: "invalid", args: []string{"--color=sometimes", "hello"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts, err := parseOptions(test.args, &strings.Builder{})
			if test.wantError {
				if err == nil {
					t.Fatal("parseOptions returned nil error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOptions: %v", err)
			}
			if opts.color != test.want {
				t.Fatalf("color = %q, want %q", opts.color, test.want)
			}
		})
	}
}
