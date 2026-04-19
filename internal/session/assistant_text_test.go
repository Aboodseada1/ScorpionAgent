package session

import "testing"

func TestSanitizeAssistantStreamDelta(t *testing.T) {
	if got := SanitizeAssistantStreamDelta("Hello there"); got != "Hello there" {
		t.Fatalf("got %q", got)
	}
	if got := SanitizeAssistantStreamDelta(`Hi.</function>add_note>`); got != "Hi." {
		t.Fatalf("got %q", got)
	}
	if got := SanitizeAssistantStreamDelta(`<function`); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeAssistantFinal(t *testing.T) {
	in := `I'm great. </function>add_note>{"text":"x"}`
	if got := SanitizeAssistantFinal(in); got != "I'm great." {
		t.Fatalf("got %q", got)
	}
}
