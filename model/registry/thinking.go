package registry

import (
	"github.com/lgxz/dora/model/openai"
	"github.com/lgxz/dora/model/openairesponses"
)

// mapChatThinking maps the neutral thinking value onto the Chat Completions
// reasoning_effort and thinking controls according to the provider policy.
// Values a provider does not support are silently ignored (not sent).
func mapChatThinking(provider string, thinking *string) (*string, *openai.ThinkingControl) {
	if thinking == nil {
		return nil, nil
	}
	value := *thinking
	if value == "off" {
		switch provider {
		case "deepseek":
			return nil, openai.NewThinkingControl("disabled")
		default:
			// openai/trust do not support off on Chat Completions; ignore.
			return nil, nil
		}
	}
	if value == "minimal" && provider == "deepseek" {
		// DeepSeek does not support minimal on Chat Completions; ignore.
		return nil, nil
	}
	return &value, nil
}

// mapResponsesThinking maps the neutral thinking value onto the Responses API
// reasoning control according to the provider policy. Values a provider does
// not support are silently ignored (not sent).
func mapResponsesThinking(provider string, thinking *string) *openairesponses.ReasoningControl {
	if thinking == nil {
		return nil
	}
	value := *thinking
	if value == "off" {
		return openairesponses.NewReasoningControl("none")
	}
	if value == "minimal" && provider == "deepseek" {
		// DeepSeek does not support minimal on the Responses API; ignore.
		return nil
	}
	return openairesponses.NewReasoningControl(value)
}
