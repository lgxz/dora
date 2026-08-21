package dora

// Role identifies the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Image is a reference to an image attached to a message. Exactly one of Path
// or URL should be set: Path refers to a local file that adapters read and
// encode, while URL is used directly as the image source.
type Image struct {
	Path string `json:"path,omitempty"` // local file path (mutually exclusive with URL)
	URL  string `json:"url,omitempty"`  // remote image URL (mutually exclusive with Path)
}

// Message is one entry in a conversation.
//
// ToolCalls is populated on assistant messages that request tools. ToolCallID
// is populated on tool messages so a model can match a result to its request.
// Images holds images attached to the message for multimodal models. Reasoning
// is populated on assistant messages from reasoning models and carries their
// chain-of-thought; adapters resend it only according to their
// provider-specific policy.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"`
	Images     []Image    `json:"images,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
