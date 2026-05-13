package main

import "testing"

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
}
