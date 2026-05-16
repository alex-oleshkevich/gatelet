package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestParseConfigAcceptsNameAndTargetBeforeFlags(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"http://127.0.0.1:3000",
		"--server", "127.0.0.1:4443",
		"--token", "dev-token",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.Name != "alex" {
		t.Fatalf("Name = %q, want alex", config.Name)
	}
	if config.Target != "http://127.0.0.1:3000" {
		t.Fatalf("Target = %q, want http://127.0.0.1:3000", config.Target)
	}
}

func TestParseConfigAcceptsTargetAfterFlags(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--token", "dev-token",
		"http://127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.Target != "http://127.0.0.1:3000" {
		t.Fatalf("Target = %q, want http://127.0.0.1:3000", config.Target)
	}
}

func TestParseConfigRejectsConflictingTargetValues(t *testing.T) {
	_, err := parseConfig([]string{
		"alex",
		"http://127.0.0.1:3000",
		"--server", "127.0.0.1:4443",
		"--to", "http://127.0.0.1:9090",
		"--token", "dev-token",
	})
	if err == nil {
		t.Fatal("parseConfig returned nil error")
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

func TestParseConfigAcceptsServerFromEnvironment(t *testing.T) {
	t.Setenv("GATELET_SERVER", "env.example.test:4443")

	config, err := parseConfig([]string{
		"alex",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.ServerAddr != "env.example.test:4443" {
		t.Fatalf("ServerAddr = %q, want env.example.test:4443", config.ServerAddr)
	}
}

func TestParseConfigServerFlagOverridesEnvironment(t *testing.T) {
	t.Setenv("GATELET_SERVER", "env.example.test:4443")

	config, err := parseConfig([]string{
		"alex",
		"--server", "flag.example.test:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.ServerAddr != "flag.example.test:4443" {
		t.Fatalf("ServerAddr = %q, want flag.example.test:4443", config.ServerAddr)
	}
}

func TestParseConfigTokenFlagOverridesEnvironment(t *testing.T) {
	t.Setenv("GATELET_TOKEN", "env-token")

	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "flag-token",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.Token != "flag-token" {
		t.Fatalf("Token = %q, want flag-token", config.Token)
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

func TestParseConfigAcceptsHTTPBasicAuth(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"http://127.0.0.1:3000",
		"--server", "127.0.0.1:4443",
		"--token", "dev-token",
		"--basic-auth", "operator:secret",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.HTTPBasicAuthUser != "operator" || config.HTTPBasicAuthPassword != "secret" {
		t.Fatalf("basic auth = %q:%q, want operator:secret", config.HTTPBasicAuthUser, config.HTTPBasicAuthPassword)
	}
}

func TestParseConfigAcceptsHTTPBasicAuthFromEnvironment(t *testing.T) {
	t.Setenv("GATELET_BASIC_AUTH", "operator:secret")

	config, err := parseConfig([]string{
		"alex",
		"http://127.0.0.1:3000",
		"--server", "127.0.0.1:4443",
		"--token", "dev-token",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if config.HTTPBasicAuthUser != "operator" || config.HTTPBasicAuthPassword != "secret" {
		t.Fatalf("basic auth = %q:%q, want operator:secret", config.HTTPBasicAuthUser, config.HTTPBasicAuthPassword)
	}
}

func TestParseConfigRejectsMalformedHTTPBasicAuth(t *testing.T) {
	for _, value := range []string{"operator", ":secret", "operator:"} {
		t.Run(value, func(t *testing.T) {
			_, err := parseConfig([]string{
				"alex",
				"http://127.0.0.1:3000",
				"--server", "127.0.0.1:4443",
				"--token", "dev-token",
				"--basic-auth", value,
			})
			if err == nil {
				t.Fatal("parseConfig returned nil error")
			}
		})
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

func TestParseConfigAcceptsInspectAPI(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--inspect",
		"--inspect-addr", "127.0.0.1:54321",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if !config.Inspect {
		t.Fatal("Inspect = false, want true")
	}
	if config.InspectAddr != "127.0.0.1:54321" {
		t.Fatalf("InspectAddr = %q, want 127.0.0.1:54321", config.InspectAddr)
	}
}

func TestParseConfigAcceptsAgentMode(t *testing.T) {
	config, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--agent",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if !config.Agent || !config.Inspect {
		t.Fatalf("Agent/Inspect = %v/%v, want true/true", config.Agent, config.Inspect)
	}
	if config.LogFormat != client.LogFormatJSONL {
		t.Fatalf("LogFormat = %q, want %q", config.LogFormat, client.LogFormatJSONL)
	}
}

func TestParseConfigRejectsInspectWithTUI(t *testing.T) {
	_, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--inspect",
		"--tui",
	})
	if err == nil {
		t.Fatal("parseConfig returned nil error")
	}
	if !strings.Contains(err.Error(), "--inspect cannot be combined with --tui") {
		t.Fatalf("error = %q, want inspect/TUI conflict", err.Error())
	}
}

func TestParseConfigRejectsAgentWithTUI(t *testing.T) {
	_, err := parseConfig([]string{
		"alex",
		"--server", "127.0.0.1:4443",
		"--to", "127.0.0.1:3000",
		"--token", "dev-token",
		"--agent",
		"--tui",
	})
	if err == nil {
		t.Fatal("parseConfig returned nil error")
	}
	if !strings.Contains(err.Error(), "--agent cannot be combined with --tui") {
		t.Fatalf("error = %q, want agent/TUI conflict", err.Error())
	}
}

func TestRunTreatsInspectAsSubcommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"inspect", "alex"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), `unknown inspect command "alex"`) {
		t.Fatalf("stderr = %q, want unknown inspect command", stderr.String())
	}
}

func TestRunAllowsCommandWordTunnelNamesWithNameFlag(t *testing.T) {
	oldClientRun := clientRun
	defer func() {
		clientRun = oldClientRun
	}()

	var names []string
	clientRun = func(ctx context.Context, config client.Config) error {
		names = append(names, config.Name)
		return nil
	}

	for _, name := range []string{"prime", "inspect"} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := run([]string{
			"--name", name,
			"--server", "127.0.0.1:4443",
			"--token", "dev-token",
			"--control-plaintext",
			"http://127.0.0.1:3000",
		}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("run for tunnel %q exit code = %d, stderr = %q", name, code, stderr.String())
		}
	}

	if len(names) != 2 || names[0] != "prime" || names[1] != "inspect" {
		t.Fatalf("client names = %+v, want prime and inspect", names)
	}
}

func TestParseConfigRejectsTCPFlag(t *testing.T) {
	_, err := parseConfig([]string{
		"pg",
		"localhost:5432",
		"--server", "wss://tun.example.test",
		"--token", "dev-token",
		"--tcp",
	})
	if err == nil {
		t.Fatal("parseConfig returned nil error")
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

func TestRunHelpPrintsUsageAndSucceeds(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	got := stdout.String()
	for _, want := range []string{
		"Usage: gatelet <name> <target> --server <addr> [flags]",
		"<target>",
		"--server",
		"--to",
		"--token",
		"--inspect",
		"--tui",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "flag: help requested") {
		t.Fatalf("help output contains fatal flag error: %q", got)
	}
}

func TestRunCompletionPrintsBashScript(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"completion", "bash"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"_gatelet_completion",
		"prime inspect completion",
		"--server",
		"--inspect-addr",
		"--preview-size",
		"status requests request replay pause resume wait capabilities openapi",
		"--json",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("completion output %q does not contain %q", got, want)
		}
	}
}

func TestRunCompletionRejectsUnknownShell(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"completion", "ksh"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), `unsupported completion shell "ksh"`) {
		t.Fatalf("stderr = %q, want unsupported shell", stderr.String())
	}
}

func TestRunInspectServesClientEvents(t *testing.T) {
	oldClientRun := clientRun
	defer func() {
		clientRun = oldClientRun
	}()

	clientRun = func(ctx context.Context, config client.Config) error {
		config.Events <- client.RequestEvent{
			ID:         12,
			Type:       client.EventRequestCompleted,
			Method:     http.MethodGet,
			RequestURI: "/inspect",
			StatusCode: http.StatusOK,
		}
		<-ctx.Done()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout bytes.Buffer
	var stderr lockedBuffer
	config := client.Config{
		Name:        "alex",
		ServerAddr:  "wss://example.test",
		Target:      "http://127.0.0.1:3000",
		Token:       "dev-token",
		Inspect:     true,
		InspectAddr: "127.0.0.1:0",
		LogFormat:   client.LogFormatText,
	}

	done := make(chan error, 1)
	go func() {
		done <- runInspect(ctx, config, &stdout, &stderr)
	}()

	apiURL, token := waitForInspectMetadata(t, &stderr)
	req, err := http.NewRequest(http.MethodGet, apiURL+"/api/requests", nil)
	if err != nil {
		t.Fatalf("new requests request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/requests returned error: %v", err)
	}
	defer resp.Body.Close()

	var requests []struct {
		ID         uint64 `json:"id"`
		RequestURI string `json:"request_uri"`
		StatusCode int    `json:"status_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&requests); err != nil {
		t.Fatalf("decode requests: %v", err)
	}
	if len(requests) != 1 || requests[0].ID != 12 || requests[0].RequestURI != "/inspect" || requests[0].StatusCode != http.StatusOK {
		t.Fatalf("requests = %+v", requests)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runInspect returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runInspect did not stop after context cancellation")
	}
}

func TestRunAgentPrintsMachineReadableStartup(t *testing.T) {
	oldClientRun := clientRun
	defer func() {
		clientRun = oldClientRun
	}()
	clientRun = func(ctx context.Context, config client.Config) error {
		<-ctx.Done()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var stdout bytes.Buffer
	var stderr lockedBuffer
	config := client.Config{
		Name:        "alex",
		ServerAddr:  "wss://example.test",
		Target:      "http://127.0.0.1:3000",
		Token:       "dev-token",
		Agent:       true,
		Inspect:     true,
		InspectAddr: "127.0.0.1:0",
		LogFormat:   client.LogFormatJSONL,
	}

	done := make(chan error, 1)
	go func() {
		done <- runInspect(ctx, config, &stdout, &stderr)
	}()
	startup := waitForAgentStartup(t, &stdout)
	if startup.Type != "startup" || startup.URL != "https://alex.example.test" || startup.Target != "http://127.0.0.1:3000" {
		t.Fatalf("startup = %+v", startup)
	}
	if startup.InspectURL == "" || startup.InspectToken == "" || startup.CapabilitiesURL == "" {
		t.Fatalf("startup missing inspect fields: %+v", startup)
	}

	var primeStdout bytes.Buffer
	var primeStderr bytes.Buffer
	code := run([]string{"prime"}, &primeStdout, &primeStderr)
	if code != 0 {
		t.Fatalf("prime exit code = %d, stderr = %q", code, primeStderr.String())
	}
	if !strings.Contains(primeStdout.String(), "Gatelet client") || !strings.Contains(primeStdout.String(), "https://alex.example.test") {
		t.Fatalf("human prime output = %q", primeStdout.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runInspect returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runInspect did not stop after context cancellation")
	}
}

func TestRunPrimePrintsHumanBriefingByDefault(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/capabilities":
			_, _ = w.Write([]byte(`{"api_version":"v1","public_url":"https://alex.example.test","target":"http://127.0.0.1:3000","actions":["pause","resume","replay"],"endpoints":["/api/status"]}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"public_url":"https://alex.example.test","target":"http://127.0.0.1:3000","paused":false,"request_count":2}`))
		case "/api/requests":
			_, _ = w.Write([]byte(`[{"id":2,"method":"GET","request_uri":"/api","status_code":200}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"prime", "--addr", api.URL, "--token", "secret-token"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"Gatelet client",
		"Public URL: https://alex.example.test",
		"Target: http://127.0.0.1:3000",
		"Requests: 2",
		"Recent requests:",
		"GET /api 200",
		"Commands:",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prime output %q does not contain %q", got, want)
		}
	}
}

func TestRunPrimePrintsJSONBriefingWithFlag(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/capabilities":
			_, _ = w.Write([]byte(`{"api_version":"v1","public_url":"https://alex.example.test","target":"http://127.0.0.1:3000","actions":["pause","resume","replay"],"endpoints":["/api/status"]}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"public_url":"https://alex.example.test","target":"http://127.0.0.1:3000","paused":false,"request_count":2}`))
		case "/api/requests":
			_, _ = w.Write([]byte(`[{"id":2,"method":"GET","request_uri":"/api","status_code":200}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"prime", "--addr", api.URL, "--token", "secret-token", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	var briefing primeBriefing
	if err := json.Unmarshal(stdout.Bytes(), &briefing); err != nil {
		t.Fatalf("decode prime briefing: %v\n%s", err, stdout.String())
	}
	if briefing.Type != "gatelet_prime" || briefing.Status.RequestCount != 2 || len(briefing.RecentRequests) != 1 {
		t.Fatalf("briefing = %+v", briefing)
	}
}

func TestRunInspectSubcommandsUseLocalAPI(t *testing.T) {
	var sawPause bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/status":
			_, _ = w.Write([]byte(`{"public_url":"https://alex.example.test","paused":false}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/capabilities":
			_, _ = w.Write([]byte(`{"actions":["pause","resume","replay"]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.1.0"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/requests":
			if got := r.URL.Query().Get("path_contains"); got != "/a b&x=1" {
				t.Fatalf("path_contains = %q, want escaped value", got)
			}
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/pause":
			sawPause = true
			_, _ = w.Write([]byte(`{"public_url":"https://alex.example.test","paused":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"inspect", "status", "--addr", api.URL, "--token", "secret-token"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"paused":false`) {
		t.Fatalf("status output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"inspect", "pause", "--addr", api.URL, "--token", "secret-token"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pause exit code = %d, stderr = %q", code, stderr.String())
	}
	if !sawPause || !strings.Contains(stdout.String(), `"paused":true`) {
		t.Fatalf("pause sawPause/output = %v/%q", sawPause, stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"inspect", "requests", "--addr", api.URL, "--token", "secret-token", "--path-contains", "/a b&x=1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("requests exit code = %d, stderr = %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("requests output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"inspect", "capabilities", "--addr", api.URL, "--token", "secret-token"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("capabilities exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"actions":["pause","resume","replay"]`) {
		t.Fatalf("capabilities output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"inspect", "openapi", "--addr", api.URL, "--token", "secret-token"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("openapi exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"openapi":"3.1.0"`) {
		t.Fatalf("openapi output = %q", stdout.String())
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type startupRecordForTest struct {
	Type            string `json:"type"`
	URL             string `json:"url"`
	Target          string `json:"target"`
	InspectURL      string `json:"inspect_url"`
	InspectToken    string `json:"inspect_token"`
	CapabilitiesURL string `json:"capabilities_url"`
}

func waitForAgentStartup(t *testing.T, stdout *bytes.Buffer) startupRecordForTest {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		lines := strings.Split(stdout.String(), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var startup startupRecordForTest
			if err := json.Unmarshal([]byte(line), &startup); err == nil && startup.Type == "startup" {
				return startup
			}
		}
		select {
		case <-deadline:
			t.Fatalf("agent startup was not printed; stdout = %q", stdout.String())
		case <-tick.C:
		}
	}
}

func waitForInspectMetadata(t *testing.T, stderr *lockedBuffer) (string, string) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var apiURL string
		var token string
		lines := strings.Split(stderr.String(), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			switch fields[0] {
			case "inspect":
				apiURL = fields[1]
			case "inspect-token":
				token = fields[1]
			}
		}
		if apiURL != "" && token != "" {
			return apiURL, token
		}
		select {
		case <-deadline:
			t.Fatalf("inspect metadata was not printed; stderr = %q", stderr.String())
		case <-tick.C:
		}
	}
}
