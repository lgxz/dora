package dora

import "testing"

func TestResponseUsageZeroValueIsNil(t *testing.T) {
	var response Response
	if response.Usage != nil {
		t.Fatalf("zero-value Response.Usage = %#v, want nil", response.Usage)
	}
}

func TestUsageFromDetails(t *testing.T) {
	cached := int64(10)
	reasoning := int64(2)
	usage := &Usage{
		InputTokens:  5,
		OutputTokens: 3,
		TotalTokens:  8,
		InputDetails: &InputTokenDetails{CachedTokens: &cached},
		OutputDetails: &OutputTokenDetails{
			ReasoningTokens: &reasoning,
		},
	}
	if usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		t.Fatalf("TotalTokens = %d, want %d", usage.TotalTokens, usage.InputTokens+usage.OutputTokens)
	}
	if usage.InputDetails == nil || usage.InputDetails.CachedTokens == nil || *usage.InputDetails.CachedTokens != 10 {
		t.Fatalf("InputDetails = %#v", usage.InputDetails)
	}
	if usage.OutputDetails == nil || usage.OutputDetails.ReasoningTokens == nil || *usage.OutputDetails.ReasoningTokens != 2 {
		t.Fatalf("OutputDetails = %#v", usage.OutputDetails)
	}
}
