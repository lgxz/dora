package dora

import "testing"

func TestCapabilityValues(t *testing.T) {
	tests := []struct {
		cap  Capability
		want string
	}{
		{CapabilityText, "text"},
		{CapabilityImageInput, "image_input"},
		{CapabilityImageOutput, "image_output"},
		{CapabilityAudioInput, "audio_input"},
		{CapabilityFileInput, "file_input"},
	}
	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		if string(tt.cap) != tt.want {
			t.Fatalf("capability %q = %q, want %q", tt.want, string(tt.cap), tt.want)
		}
		if tt.cap == "" {
			t.Fatalf("capability %q is empty", tt.want)
		}
		if _, exists := seen[string(tt.cap)]; exists {
			t.Fatalf("capability value %q is duplicated", string(tt.cap))
		}
		seen[string(tt.cap)] = struct{}{}
	}
}

func TestConstraintsZeroValue(t *testing.T) {
	var c Constraints
	if c.Provider != "" || c.Profile != "" || len(c.Needs) != 0 {
		t.Fatalf("zero Constraints = %#v", c)
	}
}
