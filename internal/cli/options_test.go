package cli

import (
	"context"
	"strings"
	"testing"
)

func TestParseOptionsWorkdir(t *testing.T) {
	opts, err := parseOptions([]string{"--workdir", "project", "hello"}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.workdir != "project" || len(opts.promptArgs) != 1 || opts.promptArgs[0] != "hello" {
		t.Fatalf("options = %#v", opts)
	}
}

func TestParseOptionsUpdateTo(t *testing.T) {
	opts, err := parseOptions([]string{"-update", "-to", "v1.2.3"}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.update || opts.updateTo != "v1.2.3" {
		t.Fatalf("options = %#v", opts)
	}
}

func TestToRequiresUpdate(t *testing.T) {
	opts, err := parseOptions([]string{"-to", "1.2.3"}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	handled, err := handleImmediate(context.Background(), opts, IO{})
	if !handled || err == nil || !strings.Contains(err.Error(), "-to requires -update") {
		t.Fatalf("handled = %v, err = %v", handled, err)
	}
}

func TestParseOptionsSetup(t *testing.T) {
	opts, err := parseOptions([]string{"--setup", "--config", "config.yaml"}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.setup || opts.configPath != "config.yaml" {
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
