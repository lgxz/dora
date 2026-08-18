package dora

// Capability names a provider-neutral model capability. It is a string value
// type so that both model/registry and model/router can reference it without
// an import cycle.
type Capability string

// Constraint capabilities implemented in v1. Reserved values are declared but
// not implemented anywhere in this release.
const (
	CapabilityText        Capability = "text"
	CapabilityImageInput  Capability = "image_input"
	CapabilityImageOutput Capability = "image_output"

	// Reserved, not implemented.
	CapabilityAudioInput Capability = "audio_input"
	CapabilityFileInput  Capability = "file_input"
)

// Constraints narrows a catalog selection. Empty fields do not participate.
// Provider and Profile are exact-name filters; Needs is satisfied only when
// the candidate supports every listed capability (AND / intersection).
type Constraints struct {
	Provider string
	Profile  string
	Needs    []Capability
}
