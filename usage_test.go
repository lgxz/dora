package dora

import "testing"

func TestOccupancy(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if got := Occupancy(nil); got != 0 {
			t.Fatalf("Occupancy(nil) = %d, want 0", got)
		}
	})

	t.Run("input only", func(t *testing.T) {
		u := &Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		if got := Occupancy(u); got != 10 {
			t.Fatalf("Occupancy = %d, want 10 (input only)", got)
		}
	})

	t.Run("zero input", func(t *testing.T) {
		if got := Occupancy(&Usage{InputTokens: 0, TotalTokens: 20}); got != 0 {
			t.Fatalf("Occupancy = %d, want 0", got)
		}
	})

	t.Run("negative input passed through", func(t *testing.T) {
		if got := Occupancy(&Usage{InputTokens: -7}); got != -7 {
			t.Fatalf("Occupancy = %d, want -7 (passed through verbatim)", got)
		}
	})
}

func TestOccupancyTotal(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if got := OccupancyTotal(nil); got != 0 {
			t.Fatalf("OccupancyTotal(nil) = %d, want 0", got)
		}
	})

	t.Run("total includes input and output", func(t *testing.T) {
		u := &Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		if got := OccupancyTotal(u); got != 15 {
			t.Fatalf("OccupancyTotal = %d, want 15 (total)", got)
		}
	})

	t.Run("zero total", func(t *testing.T) {
		if got := OccupancyTotal(&Usage{InputTokens: 10, OutputTokens: 5}); got != 0 {
			t.Fatalf("OccupancyTotal = %d, want 0", got)
		}
	})

	t.Run("negative total passed through", func(t *testing.T) {
		if got := OccupancyTotal(&Usage{TotalTokens: -3}); got != -3 {
			t.Fatalf("OccupancyTotal = %d, want -3 (passed through verbatim)", got)
		}
	})
}