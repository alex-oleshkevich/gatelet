package main

import "testing"

func TestParseTokenSpecsAcceptsActiveAndInactiveTokens(t *testing.T) {
	tokens, err := parseTokenSpecs("current=new-token,previous=old-token,inactive=disabled-token:inactive")
	if err != nil {
		t.Fatalf("parseTokenSpecs returned error: %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("len(tokens) = %d, want 3", len(tokens))
	}
	if tokens[0].ID != "current" || tokens[0].Value != "new-token" || !tokens[0].Active {
		t.Fatalf("current token = %+v, want active current", tokens[0])
	}
	if tokens[1].ID != "previous" || tokens[1].Value != "old-token" || !tokens[1].Active {
		t.Fatalf("previous token = %+v, want active previous", tokens[1])
	}
	if tokens[2].ID != "inactive" || tokens[2].Value != "disabled-token" || tokens[2].Active {
		t.Fatalf("inactive token = %+v, want inactive", tokens[2])
	}
}

func TestParseTokenSpecsRejectsInvalidTokenSpecs(t *testing.T) {
	tests := []string{
		"missing-value",
		"=secret",
		"current=",
		"bad/id=secret",
		"current=secret:expired",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := parseTokenSpecs(input); err == nil {
				t.Fatal("parseTokenSpecs returned nil error")
			}
		})
	}
}
