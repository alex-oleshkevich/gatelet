package protocol

import "testing"

func TestValidateNameAcceptsSubdomainLabel(t *testing.T) {
	if err := ValidateName("alex"); err != nil {
		t.Fatalf("ValidateName returned error: %v", err)
	}
}

func TestValidateNameRejectsInvalidLabels(t *testing.T) {
	tests := []string{
		"",
		"-alex",
		"alex-",
		"al_ex",
		"Alex",
		"alex.dev",
	}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateName(name); err == nil {
				t.Fatalf("ValidateName(%q) returned nil error", name)
			}
		})
	}
}

func TestChallengeResponseValidatesSharedTokenWithoutExposingIt(t *testing.T) {
	response := ChallengeResponse("alex", "nonce-value", "dev-token")

	if response == "dev-token" {
		t.Fatal("ChallengeResponse returned the raw token")
	}
	if !ValidChallengeResponse("alex", "nonce-value", "dev-token", response) {
		t.Fatal("ValidChallengeResponse rejected a valid response")
	}
	if ValidChallengeResponse("alex", "nonce-value", "wrong-token", response) {
		t.Fatal("ValidChallengeResponse accepted a wrong token")
	}
}

func TestParseClientChallengeResponseAcceptsTokenID(t *testing.T) {
	response, err := ParseClientChallengeResponse([]byte(`{"token_id":"current","response":"abc123"}`))
	if err != nil {
		t.Fatalf("ParseClientChallengeResponse returned error: %v", err)
	}
	if response.TokenID != "current" {
		t.Fatalf("TokenID = %q, want current", response.TokenID)
	}
	if response.EffectiveTokenID() != "current" {
		t.Fatalf("EffectiveTokenID = %q, want current", response.EffectiveTokenID())
	}
}

func TestParseClientChallengeResponseKeepsLegacyDefaultTokenID(t *testing.T) {
	response, err := ParseClientChallengeResponse([]byte(`{"response":"abc123"}`))
	if err != nil {
		t.Fatalf("ParseClientChallengeResponse returned error: %v", err)
	}
	if response.EffectiveTokenID() != DefaultTokenID {
		t.Fatalf("EffectiveTokenID = %q, want %q", response.EffectiveTokenID(), DefaultTokenID)
	}
}

func TestParseClientChallengeResponseRejectsInvalidTokenID(t *testing.T) {
	if _, err := ParseClientChallengeResponse([]byte(`{"token_id":"bad/id","response":"abc123"}`)); err == nil {
		t.Fatal("ParseClientChallengeResponse returned nil error")
	}
}

func TestParseClientHelloValidatesName(t *testing.T) {
	hello, err := ParseClientHello([]byte(`{"name":"alex","protocol_version":1,"client_version":"gatelet-test"}`))
	if err != nil {
		t.Fatalf("ParseClientHello returned error: %v", err)
	}
	if hello.Name != "alex" {
		t.Fatalf("Name = %q, want %q", hello.Name, "alex")
	}
	if hello.TunnelType != TunnelTypeHTTP {
		t.Fatalf("TunnelType = %q, want %q", hello.TunnelType, TunnelTypeHTTP)
	}

	if _, err := ParseClientHello([]byte(`{"name":"alex.dev"}`)); err == nil {
		t.Fatal("ParseClientHello accepted invalid name")
	}
}

func TestParseClientHelloAcceptsTCPTunnel(t *testing.T) {
	hello, err := ParseClientHello([]byte(`{"name":"pg","protocol_version":1,"client_version":"gatelet-test","tunnel_type":"tcp","remote_port":15432}`))
	if err != nil {
		t.Fatalf("ParseClientHello returned error: %v", err)
	}
	if hello.TunnelType != TunnelTypeTCP {
		t.Fatalf("TunnelType = %q, want %q", hello.TunnelType, TunnelTypeTCP)
	}
	if hello.RemotePort != 15432 {
		t.Fatalf("RemotePort = %d, want 15432", hello.RemotePort)
	}
}

func TestParseClientHelloRejectsInvalidTCPTunnel(t *testing.T) {
	tests := []string{
		`{"name":"pg","protocol_version":1,"client_version":"gatelet-test","tunnel_type":"tcp"}`,
		`{"name":"pg","protocol_version":1,"client_version":"gatelet-test","tunnel_type":"tcp","remote_port":70000}`,
		`{"name":"pg","protocol_version":1,"client_version":"gatelet-test","tunnel_type":"http","remote_port":15432}`,
		`{"name":"pg","protocol_version":1,"client_version":"gatelet-test","tunnel_type":"udp","remote_port":15432}`,
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseClientHello([]byte(input)); err == nil {
				t.Fatal("ParseClientHello returned nil error")
			}
		})
	}
}

func TestParseClientHelloRequiresSupportedProtocolVersion(t *testing.T) {
	if _, err := ParseClientHello([]byte(`{"name":"alex","client_version":"gatelet-test"}`)); err == nil {
		t.Fatal("ParseClientHello accepted missing protocol version")
	}
	if _, err := ParseClientHello([]byte(`{"name":"alex","protocol_version":999,"client_version":"gatelet-test"}`)); err == nil {
		t.Fatal("ParseClientHello accepted unsupported protocol version")
	}
}

func TestParseClientHelloRequiresClientVersion(t *testing.T) {
	if _, err := ParseClientHello([]byte(`{"name":"alex","protocol_version":1}`)); err == nil {
		t.Fatal("ParseClientHello accepted missing client version")
	}
}
