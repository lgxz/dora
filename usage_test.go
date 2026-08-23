package dora

import "testing"

func TestInputTokens(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if got := InputTokens(nil); got != 0 {
			t.Fatalf("InputTokens(nil) = %d, want 0", got)
		}
	})

	t.Run("input only", func(t *testing.T) {
		u := &Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		if got := InputTokens(u); got != 10 {
			t.Fatalf("InputTokens = %d, want 10 (input only)", got)
		}
	})

	t.Run("zero input", func(t *testing.T) {
		if got := InputTokens(&Usage{InputTokens: 0, TotalTokens: 20}); got != 0 {
			t.Fatalf("InputTokens = %d, want 0", got)
		}
	})

	t.Run("negative input passed through", func(t *testing.T) {
		if got := InputTokens(&Usage{InputTokens: -7}); got != -7 {
			t.Fatalf("InputTokens = %d, want -7 (passed through verbatim)", got)
		}
	})
}

func TestTotalTokens(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if got := TotalTokens(nil); got != 0 {
			t.Fatalf("TotalTokens(nil) = %d, want 0", got)
		}
	})

	t.Run("total includes input and output", func(t *testing.T) {
		u := &Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		if got := TotalTokens(u); got != 15 {
			t.Fatalf("TotalTokens = %d, want 15 (total)", got)
		}
	})

	t.Run("zero total", func(t *testing.T) {
		if got := TotalTokens(&Usage{InputTokens: 10, OutputTokens: 5}); got != 0 {
			t.Fatalf("TotalTokens = %d, want 0", got)
		}
	})

	t.Run("negative total passed through", func(t *testing.T) {
		if got := TotalTokens(&Usage{TotalTokens: -3}); got != -3 {
			t.Fatalf("TotalTokens = %d, want -3 (passed through verbatim)", got)
		}
	})
}