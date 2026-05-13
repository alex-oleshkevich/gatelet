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

func TestParseClientHelloValidatesName(t *testing.T) {
	hello, err := ParseClientHello([]byte(`{"name":"alex","protocol_version":1,"client_version":"gatelet-test"}`))
	if err != nil {
		t.Fatalf("ParseClientHello returned error: %v", err)
	}
	if hello.Name != "alex" {
		t.Fatalf("Name = %q, want %q", hello.Name, "alex")
	}

	if _, err := ParseClientHello([]byte(`{"name":"alex.dev"}`)); err == nil {
		t.Fatal("ParseClientHello accepted invalid name")
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
