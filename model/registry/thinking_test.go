package registry

import "testing"

func TestMapChatThinkingOpenRouterOff(t *testing.T) {
	off := "off"
	effort, thinking := mapChatThinking("openrouter", &off)
	if effort == nil || *effort != "none" || thinking != nil {
		t.Fatalf("mapChatThinking(openrouter, off) = (%v, %#v), want (none, nil)", effort, thinking)
	}
}

func TestMapDeepSeekExtendedThinking(t *testing.T) {
	for _, value := range []string{"xhigh", "max"} {
		t.Run(value, func(t *testing.T) {
			effort, thinking := mapChatThinking("deepseek", &value)
			if effort == nil || *effort != value || thinking != nil {
				t.Fatalf("mapChatThinking(deepseek, %s) = (%v, %#v)", value, effort, thinking)
			}
			reasoning := mapResponsesThinking("deepseek", &value)
			if reasoning == nil || reasoning.Effort != value {
				t.Fatalf("mapResponsesThinking(deepseek, %s) = %#v", value, reasoning)
			}
		})
	}
}
