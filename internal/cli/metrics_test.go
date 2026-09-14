package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lgxz/dora"
)

func TestAggregateUsagesSumsReportedCalls(t *testing.T) {
	firstCached := int64(7)
	secondCached := int64(11)
	firstReasoning := int64(2)
	secondReasoning := int64(3)
	got := aggregateUsages([]*dora.Usage{
		{
			InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
			InputDetails:  &dora.InputTokenDetails{CachedTokens: &firstCached},
			OutputDetails: &dora.OutputTokenDetails{ReasoningTokens: &firstReasoning},
		},
		nil,
		{
			InputTokens: 20, OutputTokens: 3, TotalTokens: 23,
			InputDetails:  &dora.InputTokenDetails{CachedTokens: &secondCached},
			OutputDetails: &dora.OutputTokenDetails{ReasoningTokens: &secondReasoning},
		},
	})
	if got == nil {
		t.Fatal("aggregateUsages returned nil")
	}
	if got.InputTokens != 30 || got.OutputTokens != 5 || got.TotalTokens != 35 {
		t.Fatalf("aggregate = %#v", got)
	}
	if got.InputDetails == nil || got.InputDetails.CachedTokens == nil || *got.InputDetails.CachedTokens != 18 {
		t.Fatalf("input details = %#v", got.InputDetails)
	}
	if got.OutputDetails == nil || got.OutputDetails.ReasoningTokens == nil || *got.OutputDetails.ReasoningTokens != 5 {
		t.Fatalf("output details = %#v", got.OutputDetails)
	}
}

func TestAggregateUsagesPreservesUnknownCacheUsage(t *testing.T) {
	got := aggregateUsages([]*dora.Usage{{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}})
	if got == nil || got.InputDetails != nil {
		t.Fatalf("aggregate = %#v, want usage with unknown input details", got)
	}
	if aggregateUsages([]*dora.Usage{nil, nil}) != nil {
		t.Fatal("all-nil usage should remain nil")
	}
}

func TestWriteMetricsFileWritesNullWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := writeMetricsFile(path, dora.NewTurn("hello")); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var usage *dora.Usage
	if err := json.Unmarshal(contents, &usage); err != nil {
		t.Fatal(err)
	}
	if usage != nil {
		t.Fatalf("usage = %#v, want nil", usage)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o, want 600", info.Mode().Perm())
	}
}
