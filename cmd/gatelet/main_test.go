package main

import (
	"bytes"
	"strings"
	"testing"

	"gatelet/internal/client"
)

func TestParseConfigAcceptsNameBeforeFlags(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}

	if config.Name != "alex" {
		t.Fatalf("Name = %q, want %q", config.Name, "alex")
	}
	if config.ServerAddr != "127.0.0.1:4443" {
		t.Fatalf("ServerAddr = %q, want %q", config.ServerAddr, "127.0.0.1:4443")
	}
	if config.Target != "127.0.0.1:3000" {
		t.Fatalf("Target = %q, want %q", config.Target, "127.0.0.1:3000")
	}
	if config.Token != "dev-token" {
		t.Fatalf("Token = %q, want %q", config.Token, "dev-token")
	}
	if config.LogFormat != client.LogFormatText {
		t.Fatalf("LogFormat = %q, want %q", config.LogFormat, client.LogFormatText)
	}
	if !config.ControlTLS {
		t.Fatal("ControlTLS = false, want true")
	}
}

func TestParseConfigAcceptsTokenFromEnvironment(t *testing.T) {
	t.Setenv("GATELET_TOKEN", "env-token")

	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.Token != "env-token" {
		t.Fatalf("Token = %q, want env-token", config.Token)
	}
}

func TestParseConfigAcceptsTokenID(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--token-id", "current",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.TokenID != "current" {
		t.Fatalf("TokenID = %q, want current", config.TokenID)
	}
}

func TestParseConfigAcceptsTokenIDFromEnvironment(t *testing.T) {
	t.Setenv("GATELET_TOKEN", "env-token")
	t.Setenv("GATELET_TOKEN_ID", "previous")

	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.TokenID != "previous" {
		t.Fatalf("TokenID = %q, want previous", config.TokenID)
	}
}

func TestParseConfigAcceptsLogFormat(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--log-format", "jsonl",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.LogFormat != client.LogFormatJSONL {
		t.Fatalf("LogFormat = %q, want %q", config.LogFormat, client.LogFormatJSONL)
	}
}

func TestParseConfigAcceptsPreviewSize(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--preview-size", "8192",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.PreviewLimit != 8192 {
		t.Fatalf("PreviewLimit = %d, want 8192", config.PreviewLimit)
	}
}

func TestParseConfigRejectsUnknownLogFormat(t *testing.T) {
	_, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--log-format", "xml",
	})
	if err == nil {
		t.Fatal("parseConfig returned nil error")
	}
}

func TestParseConfigAcceptsControlPlaintext(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--control-plaintext",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.ControlTLS {
		t.Fatal("ControlTLS = true, want false")
	}
}

func TestParseConfigRejectsTLSOptionsWithControlPlaintext(t *testing.T) {
	_, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--control-plaintext",
		"--control-ca", "ca.pem",
	})
	if err == nil {
		t.Fatal("parseConfig returned nil error")
	}
}

func TestWriteStartupText(t *testing.T) {
	var buf bytes.Buffer
	writeStartup(&buf, client.Config{
		Name:       "alex",
		ServerAddr: "tun.aresa.me:4443",
		Target:     "http://127.0.0.1:3000",
		LogFormat:  client.LogFormatText,
	})

	got := buf.String()
	want := "url https://alex.tun.aresa.me\ntarget http://127.0.0.1:3000\n"
	if got != want {
		t.Fatalf("startup output = %q, want %q", got, want)
	}
}

func TestWriteStartupJSON(t *testing.T) {
	var buf bytes.Buffer
	writeStartup(&buf, client.Config{
		Name:       "alex",
		ServerAddr: "tun.aresa.me:4443",
		Target:     "http://127.0.0.1:3000",
		LogFormat:  client.LogFormatJSON,
	})

	got := buf.String()
	for _, want := range []string{
		`"type":"startup"`,
		`"url":"https://alex.tun.aresa.me"`,
		`"target":"http://127.0.0.1:3000"`,
		`"log_format":"json"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("startup JSON %q does not contain %s", got, want)
		}
	}
}
