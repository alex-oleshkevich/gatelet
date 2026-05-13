package protocol

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	HandshakeOK  = "OK\n"
	HandshakeErr = "ERR authentication failed\n"
)

type ClientHello struct {
	Name string `json:"name"`
}

type ServerChallenge struct {
	Nonce string `json:"nonce"`
}

type ClientChallengeResponse struct {
	Response string `json:"response"`
}

func NewNonce() (string, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(nonce[:]), nil
}

func ParseClientHello(data []byte) (ClientHello, error) {
	var hello ClientHello
	if err := json.Unmarshal(data, &hello); err != nil {
		return ClientHello{}, fmt.Errorf("parse client hello: %w", err)
	}
	if err := ValidateName(hello.Name); err != nil {
		return ClientHello{}, err
	}

	return hello, nil
}

func ParseServerChallenge(data []byte) (ServerChallenge, error) {
	var challenge ServerChallenge
	if err := json.Unmarshal(data, &challenge); err != nil {
		return ServerChallenge{}, fmt.Errorf("parse server challenge: %w", err)
	}
	if challenge.Nonce == "" {
		return ServerChallenge{}, errors.New("nonce is required")
	}

	return challenge, nil
}

func ParseClientChallengeResponse(data []byte) (ClientChallengeResponse, error) {
	var response ClientChallengeResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ClientChallengeResponse{}, fmt.Errorf("parse challenge response: %w", err)
	}
	if response.Response == "" {
		return ClientChallengeResponse{}, errors.New("response is required")
	}

	return response, nil
}

func ChallengeResponse(name, nonce, token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(name))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

func ValidChallengeResponse(name, nonce, token, response string) bool {
	expected := ChallengeResponse(name, nonce, token)
	return hmac.Equal([]byte(expected), []byte(response))
}

func ValidateName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > 63 {
		return errors.New("name must be at most 63 characters")
	}

	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i != 0 && i != len(name)-1:
		default:
			return fmt.Errorf("invalid tunnel name %q", name)
		}
	}

	return nil
}

func ReadLine(r io.Reader, maxBytes int) ([]byte, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			line = append(line, buf[0])
			if len(line) > maxBytes {
				return nil, errors.New("line is too long")
			}
			if buf[0] == '\n' {
				return line, nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("read line: %w", err)
		}
	}
}
