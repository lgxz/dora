package registry

import "testing"

func TestMapChatThinkingOpenRouterOff(t *testing.T) {
	off := "off"
	effort, thinking := mapChatThinking("openrouter", &off)
	if effort == nil || *effort != "none" || thinking != nil {
		t.Fatalf("mapChatThinking(openrouter, off) = (%v, %#v), want (none, nil)", effort, thinking)
	}
}
