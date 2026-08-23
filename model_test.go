package dora

import "testing"

func TestResponseUsageZeroValueIsNil(t *testing.T) {
	var response Response
	if response.Usage != nil {
		t.Fatalf("zero-value Response.Usage = %#v, want nil", response.Usage)
	}
}

func TestUsageFromDetails(t *testing.T) {
	inputReasoning := int64(10)
	usage := &Usage{
		InputTokens:  5,
		OutputTokens: 3,
		TotalTokens:  8,
		InputDetails: &TokenDetails{ReasoningTokens: &inputReasoning},
	}
	if usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		t.Fatalf("TotalTokens = %d, want %d", usage.TotalTokens, usage.InputTokens+usage.OutputTokens)
	}
	if usage.InputDetails == nil || usage.InputDetails.ReasoningTokens == nil || *usage.InputDetails.ReasoningTokens != 10 {
		t.Fatalf("InputDetails = %#v", usage.InputDetails)
	}
	if usage.OutputDetails != nil {
		t.Fatalf("OutputDetails = %#v, want nil", usage.OutputDetails)
	}
}
