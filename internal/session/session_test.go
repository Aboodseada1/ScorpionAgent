package session

import "testing"

func TestExtractSentence(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantSent string
		wantRest string
		wantOK   bool
	}{
		// --- The golden case from the user complaint -------------------
		// Old code flushed on the first comma, causing piper to spawn a
		// subprocess per phrase ("I'm sorry,") and giving audible pauses.
		{
			name:     "comma does not flush",
			input:    "I'm sorry, I think",
			wantSent: "",
			wantRest: "I'm sorry, I think",
			wantOK:   false,
		},
		{
			name:     "period with space after flushes whole sentence",
			input:    "I'm sorry, I think there was a technical issue. How can I",
			wantSent: "I'm sorry, I think there was a technical issue.",
			wantRest: "How can I",
			wantOK:   true,
		},
		{
			name:     "question mark flushes",
			input:    "Are you available tomorrow? I can also",
			wantSent: "Are you available tomorrow?",
			wantRest: "I can also",
			wantOK:   true,
		},
		{
			name:     "exclamation flushes",
			input:    "That sounds great for both of us! Let me",
			wantSent: "That sounds great for both of us!",
			wantRest: "Let me",
			wantOK:   true,
		},
		// --- Guards against false splits ------------------------------
		{
			name:     "abbreviation does not split the sentence",
			input:    "Dr. Smith called about the offer earlier today. Want me to return it?",
			wantSent: "Dr. Smith called about the offer earlier today.",
			wantRest: "Want me to return it?",
			wantOK:   true,
		},
		{
			name:     "short sentence waits for more (below min chars before terminator)",
			input:    "Hello.",
			wantSent: "",
			wantRest: "Hello.",
			wantOK:   false,
		},
		{
			name:     "no trailing space means no flush (v1.2)",
			input:    "See version v1.2 for",
			wantSent: "",
			wantRest: "See version v1.2 for",
			wantOK:   false,
		},
		// --- End of stream ---------------------------------------------
		{
			name:     "trailing final sentence at EOF",
			input:    "And that's everything you need to know.",
			wantSent: "And that's everything you need to know.",
			wantRest: "",
			wantOK:   true,
		},
		// --- Short fragment waits for more ----------------------------
		{
			name:     "very short sentence waits (Ok.)",
			input:    "Ok. ",
			wantSent: "",
			wantRest: "Ok. ",
			wantOK:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sent, rest, ok := extractSentence(tc.input)
			if sent != tc.wantSent || rest != tc.wantRest || ok != tc.wantOK {
				t.Fatalf("extractSentence(%q)\n  got  (%q, %q, %v)\n  want (%q, %q, %v)",
					tc.input, sent, rest, ok, tc.wantSent, tc.wantRest, tc.wantOK)
			}
		})
	}
}
