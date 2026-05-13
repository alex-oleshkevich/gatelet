package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerForFormatWritesText(t *testing.T) {
	var buf bytes.Buffer
	logger, err := loggerForFormat("text", &buf)
	if err != nil {
		t.Fatalf("loggerForFormat returned error: %v", err)
	}

	logger.Info("gateletd test", "name", "alex")

	got := buf.String()
	if !strings.Contains(got, "msg=\"gateletd test\"") || !strings.Contains(got, "name=alex") {
		t.Fatalf("text log = %q, want slog text record", got)
	}
}

func TestLoggerForFormatWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	logger, err := loggerForFormat("json", &buf)
	if err != nil {
		t.Fatalf("loggerForFormat returned error: %v", err)
	}

	logger.Info("gateletd test", "name", "alex")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("json log is invalid JSON: %v", err)
	}
	if record[slog.MessageKey] != "gateletd test" || record["name"] != "alex" {
		t.Fatalf("json log = %#v, want message and name", record)
	}
}

func TestLoggerForFormatRejectsUnknownFormat(t *testing.T) {
	if _, err := loggerForFormat("jsonl", &bytes.Buffer{}); err == nil {
		t.Fatal("loggerForFormat returned nil error")
	}
}

func TestParseNameListAcceptsCommaSeparatedNames(t *testing.T) {
	names, err := parseNameList("alex,kyc,api-v2")
	if err != nil {
		t.Fatalf("parseNameList returned error: %v", err)
	}
	want := []string{"alex", "kyc", "api-v2"}
	if len(names) != len(want) {
		t.Fatalf("len(names) = %d, want %d", len(names), len(want))
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestParseNameListRejectsInvalidNames(t *testing.T) {
	if _, err := parseNameList("alex,www.example"); err == nil {
		t.Fatal("parseNameList returned nil error")
	}
}

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
