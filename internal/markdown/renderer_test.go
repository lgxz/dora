package markdown

import (
	"strings"
	"testing"
)

func TestRenderFormatsMarkdownWithoutColor(t *testing.T) {
	rendered, err := Render("# Result\n\nA **useful** answer.\n\n```go\nfmt.Println(\"dora\")\n```", Options{
		Width: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Result", "useful", `fmt.Println("dora")`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered output %q does not contain %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "**") || strings.Contains(rendered, "\x1b[") {
		t.Fatalf("rendered output contains Markdown or ANSI markup: %q", rendered)
	}
	if !strings.HasSuffix(rendered, "\n") || strings.HasSuffix(rendered, "\n\n") {
		t.Fatalf("rendered output has unexpected trailing newlines: %q", rendered)
	}
}

func TestRenderUsesANSIWhenColorIsEnabled(t *testing.T) {
	rendered, err := Render("# Result", Options{Color: true, DarkBackground: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("rendered output = %q, want ANSI styling", rendered)
	}
}

func TestNormalizeWidth(t *testing.T) {
	for _, test := range []struct {
		input int
		want  int
	}{
		{input: 0, want: defaultWidth},
		{input: 10, want: minimumWidth},
		{input: 72, want: 72},
		{input: 200, want: maximumWidth},
	} {
		if got := normalizeWidth(test.input); got != test.want {
			t.Fatalf("normalizeWidth(%d) = %d, want %d", test.input, got, test.want)
		}
	}
}
