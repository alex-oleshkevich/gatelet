package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gatelet/internal/client"
	"gatelet/internal/inspect"
	"gatelet/internal/protocol"
	"gatelet/internal/tui"
)

var clientRun = client.Run

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	if isSubcommandInvocation(args) {
		switch args[0] {
		case "prime":
			if err := runPrimeCommand(args[1:], stdout); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		case "inspect":
			if err := runInspectCommand(args[1:], stdout); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		case "completion":
			if err := runCompletionCommand(args[1:], stdout); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
	}

	config, err := parseConfig(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(stdout)
			return 0
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if config.Inspect {
		if err := runInspect(ctx, config, stdout, stderr); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	if config.TUI {
		if err := tui.Run(ctx, config); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	writeStartup(stdout, config)
	config.RequestLog = stdout
	config.StatusLog = stderr
	if err := clientRun(ctx, config); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func isSubcommandInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "prime":
		return true
	case "inspect":
		return true
	case "completion":
		return true
	default:
		return false
	}
}

func runInspect(ctx context.Context, config client.Config, stdout, stderr io.Writer) error {
	if config.InspectAddr == "" {
		config.InspectAddr = "127.0.0.1:0"
	}
	ln, err := inspect.Listen(config.InspectAddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	store := inspect.NewStore()
	pause := client.NewPauseController()
	inspectURL := "http://" + ln.Addr().String()
	if config.InspectToken == "" {
		token, err := generateInspectToken()
		if err != nil {
			return err
		}
		config.InspectToken = token
	}
	api := inspect.NewServer(inspect.Config{
		Name:         config.Name,
		PublicURL:    client.PublicURL(config.Name, config.Domain, config.ServerAddr),
		Target:       config.Target,
		Token:        config.InspectToken,
		PreviewLimit: config.PreviewLimit,
	}, store, pause)

	events := make(chan client.RequestEvent, 256)
	config.Events = events
	config.PauseController = pause
	config.RequestLog = stdout
	config.StatusLog = stderr

	config.InspectAddr = inspectURL
	errs := make(chan error, 2)
	go func() {
		errs <- api.Serve(runCtx, ln)
	}()
	if config.Agent {
		if err := writeAgentState(config); err != nil {
			return err
		}
	}
	writeStartup(stdout, config)
	if !config.Agent {
		_, _ = fmt.Fprintf(stderr, "inspect %s\n", inspectURL)
		_, _ = fmt.Fprintf(stderr, "inspect-token %s\n", config.InspectToken)
	}

	go func() {
		errs <- clientRun(runCtx, config)
	}()

	for {
		select {
		case event := <-events:
			store.Apply(event)
		case err := <-errs:
			cancel()
			return err
		case <-runCtx.Done():
			return nil
		}
	}
}

func parseConfig(args []string) (client.Config, error) {
	var config client.Config
	logFormat := string(client.LogFormatText)
	controlPlaintext := false
	positionalTarget := ""
	basicAuth := ""

	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		config.Name = args[0]
		args = args[1:]
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positionalTarget = args[0]
		args = args[1:]
	}

	flags := flag.NewFlagSet("gatelet", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&config.Name, "name", config.Name, "public tunnel name")
	flags.StringVar(&config.ServerAddr, "server", "", "gateletd control address")
	flags.StringVar(&config.Target, "to", "", "local target address")
	flags.StringVar(&config.Token, "token", "", "shared tunnel authentication token")
	flags.StringVar(&config.TokenID, "token-id", "", "token identifier for daemon-side rotation")
	flags.StringVar(&config.Domain, "domain", "", "public tunnel domain, inferred from --server when empty")
	flags.StringVar(&logFormat, "log-format", logFormat, "plain-mode request log format: text, json, or jsonl")
	flags.IntVar(&config.PreviewLimit, "preview-size", 0, "maximum request/response body preview bytes captured for TUI and logs")
	flags.StringVar(&basicAuth, "basic-auth", "", "protect HTTP tunnel with Basic Auth credentials as user:password")
	flags.BoolVar(&config.Inspect, "inspect", false, "serve a local request inspection and control API")
	flags.StringVar(&config.InspectAddr, "inspect-addr", "127.0.0.1:0", "local inspect API listen address")
	flags.BoolVar(&controlPlaintext, "control-plaintext", controlPlaintext, "disable TLS for the control connection")
	flags.StringVar(&config.ControlCACertFile, "control-ca", "", "PEM CA bundle for verifying the control server")
	flags.StringVar(&config.ControlServerName, "control-server-name", "", "TLS server name for the control connection")
	flags.BoolVar(&config.ControlInsecureSkipVerify, "control-insecure-skip-verify", false, "skip control TLS certificate verification")
	flags.BoolVar(&config.TUI, "tui", false, "show live tunnel dashboard")
	flags.BoolVar(&config.Agent, "agent", false, "run in machine-readable agent mode")
	if err := flags.Parse(args); err != nil {
		return client.Config{}, err
	}
	config.ControlTLS = !controlPlaintext
	parsedLogFormat, err := client.ParseLogFormat(logFormat)
	if err != nil {
		return client.Config{}, err
	}
	config.LogFormat = parsedLogFormat
	if !config.ControlTLS {
		if config.ControlCACertFile != "" {
			return client.Config{}, fmt.Errorf("--control-ca requires TLS")
		}
		if config.ControlServerName != "" {
			return client.Config{}, fmt.Errorf("--control-server-name requires TLS")
		}
		if config.ControlInsecureSkipVerify {
			return client.Config{}, fmt.Errorf("--control-insecure-skip-verify requires TLS")
		}
	}

	remaining := flags.Args()
	if config.Name == "" && len(remaining) > 0 {
		config.Name = remaining[0]
		remaining = remaining[1:]
	}
	if positionalTarget == "" && len(remaining) > 0 {
		positionalTarget = remaining[0]
		remaining = remaining[1:]
	}
	if len(remaining) > 0 {
		return client.Config{}, fmt.Errorf("unexpected positional argument %q", remaining[0])
	}
	if config.Name == "" {
		return client.Config{}, fmt.Errorf("--name or positional name is required")
	}
	if config.ServerAddr == "" {
		config.ServerAddr = os.Getenv("GATELET_SERVER")
	}
	if config.ServerAddr == "" {
		return client.Config{}, fmt.Errorf("--server or GATELET_SERVER is required")
	}
	if config.Target != "" && positionalTarget != "" && config.Target != positionalTarget {
		return client.Config{}, fmt.Errorf("target specified both positionally and with --to")
	}
	if config.Target == "" {
		config.Target = positionalTarget
	}
	if config.Target == "" {
		return client.Config{}, fmt.Errorf("target positional argument or --to is required")
	}
	if config.PreviewLimit < 0 {
		return client.Config{}, fmt.Errorf("--preview-size must be non-negative")
	}
	if config.Inspect && config.TUI {
		return client.Config{}, fmt.Errorf("--inspect cannot be combined with --tui")
	}
	if config.Agent {
		if config.TUI {
			return client.Config{}, fmt.Errorf("--agent cannot be combined with --tui")
		}
		config.Inspect = true
		config.LogFormat = client.LogFormatJSONL
	}
	if config.Token == "" {
		config.Token = os.Getenv("GATELET_TOKEN")
	}
	if config.Token == "" {
		return client.Config{}, fmt.Errorf("--token or GATELET_TOKEN is required")
	}
	if config.TokenID == "" {
		config.TokenID = os.Getenv("GATELET_TOKEN_ID")
	}
	if config.TokenID == "" {
		config.TokenID = protocol.DefaultTokenID
	}
	if basicAuth == "" {
		basicAuth = os.Getenv("GATELET_BASIC_AUTH")
	}
	if basicAuth != "" {
		user, password, err := parseBasicAuthSpec(basicAuth)
		if err != nil {
			return client.Config{}, err
		}
		config.HTTPBasicAuthUser = user
		config.HTTPBasicAuthPassword = password
	}

	return config, nil
}

type inspectCommandOptions struct {
	Addr        string
	Token       string
	Limit       int
	SinceID     uint64
	Method      string
	Status      int
	ErrorKind   string
	Path        string
	Event       string
	Timeout     time.Duration
	TrailingArg string
	JSON        bool
}

type primeBriefing struct {
	Type           string            `json:"type"`
	Capabilities   json.RawMessage   `json:"capabilities"`
	Status         statusBriefing    `json:"status"`
	RecentRequests []json.RawMessage `json:"recent_requests"`
	Commands       []string          `json:"commands"`
}

type statusBriefing struct {
	PublicURL    string `json:"public_url"`
	Target       string `json:"target"`
	Paused       bool   `json:"paused"`
	QueueDepth   int    `json:"queue_depth"`
	RequestCount int    `json:"request_count"`
}

type requestBriefing struct {
	ID         uint64 `json:"id"`
	Method     string `json:"method"`
	RequestURI string `json:"request_uri"`
	StatusCode int    `json:"status_code"`
	Error      string `json:"error"`
}

type agentState struct {
	InspectURL   string    `json:"inspect_url"`
	InspectToken string    `json:"inspect_token"`
	PublicURL    string    `json:"public_url"`
	Target       string    `json:"target"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func runPrimeCommand(args []string, stdout io.Writer) error {
	options, err := parseInspectCommandOptions("gatelet prime", args, false)
	if err != nil {
		return err
	}
	if err := applyAgentStateDefaults(&options); err != nil {
		return err
	}
	capabilities, err := inspectAPIRequest(context.Background(), http.MethodGet, options, "/api/capabilities", nil)
	if err != nil {
		return err
	}
	statusData, err := inspectAPIRequest(context.Background(), http.MethodGet, options, "/api/status", nil)
	if err != nil {
		return err
	}
	requests, err := inspectAPIRequest(context.Background(), http.MethodGet, options, "/api/requests?limit=10", nil)
	if err != nil {
		return err
	}

	var status statusBriefing
	if err := json.Unmarshal(statusData, &status); err != nil {
		return fmt.Errorf("decode status: %w", err)
	}
	var recent []json.RawMessage
	if err := json.Unmarshal(requests, &recent); err != nil {
		return fmt.Errorf("decode recent requests: %w", err)
	}
	var recentRequests []requestBriefing
	if err := json.Unmarshal(requests, &recentRequests); err != nil {
		return fmt.Errorf("decode recent request summary: %w", err)
	}
	briefing := primeBriefing{
		Type:           "gatelet_prime",
		Capabilities:   capabilities,
		Status:         status,
		RecentRequests: recent,
		Commands: []string{
			"gatelet inspect status --addr <url> --token <token>",
			"gatelet inspect capabilities --addr <url> --token <token>",
			"gatelet inspect requests --addr <url> --token <token>",
			"gatelet inspect request <id> --addr <url> --token <token>",
			"gatelet inspect replay <id> --addr <url> --token <token>",
			"gatelet inspect pause --addr <url> --token <token>",
			"gatelet inspect resume --addr <url> --token <token>",
			"gatelet inspect wait --addr <url> --token <token> --event request_failed",
			"gatelet inspect openapi --addr <url> --token <token>",
		},
	}
	if !options.JSON {
		return writeHumanPrime(stdout, options, status, recentRequests, briefing.Commands)
	}
	return json.NewEncoder(stdout).Encode(briefing)
}

func runInspectCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("inspect command is required")
	}
	command := args[0]
	if !isInspectCommand(command) {
		return fmt.Errorf("unknown inspect command %q", command)
	}
	options, err := parseInspectCommandOptions("gatelet inspect "+command, args[1:], inspectCommandRequiresID(command))
	if err != nil {
		return err
	}
	if err := applyAgentStateDefaults(&options); err != nil {
		return err
	}

	switch command {
	case "status":
		data, err := inspectAPIRequest(context.Background(), http.MethodGet, options, "/api/status", nil)
		return writeInspectResponse(stdout, data, err)
	case "capabilities":
		data, err := inspectAPIRequest(context.Background(), http.MethodGet, options, "/api/capabilities", nil)
		return writeInspectResponse(stdout, data, err)
	case "requests":
		data, err := inspectAPIRequest(context.Background(), http.MethodGet, options, requestsPath(options), nil)
		return writeInspectResponse(stdout, data, err)
	case "request":
		data, err := inspectAPIRequest(context.Background(), http.MethodGet, options, "/api/requests/"+options.TrailingArg, nil)
		return writeInspectResponse(stdout, data, err)
	case "replay":
		data, err := inspectAPIRequest(context.Background(), http.MethodPost, options, "/api/requests/"+options.TrailingArg+"/replay", strings.NewReader(`{}`))
		return writeInspectResponse(stdout, data, err)
	case "pause":
		data, err := inspectAPIRequest(context.Background(), http.MethodPost, options, "/api/pause", strings.NewReader(`{}`))
		return writeInspectResponse(stdout, data, err)
	case "resume":
		data, err := inspectAPIRequest(context.Background(), http.MethodPost, options, "/api/resume", strings.NewReader(`{}`))
		return writeInspectResponse(stdout, data, err)
	case "openapi":
		data, err := inspectAPIRequest(context.Background(), http.MethodGet, options, "/openapi.json", nil)
		return writeInspectResponse(stdout, data, err)
	case "wait":
		return runInspectWait(stdout, options)
	default:
		return fmt.Errorf("unknown inspect command %q", command)
	}
}

func isInspectCommand(command string) bool {
	switch command {
	case "status", "capabilities", "requests", "request", "replay", "pause", "resume", "openapi", "wait":
		return true
	default:
		return false
	}
}

func inspectCommandRequiresID(command string) bool {
	return command == "request" || command == "replay"
}

func runCompletionCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("completion shell is required")
	}
	if len(args) > 1 {
		return fmt.Errorf("unexpected argument %q", args[1])
	}
	switch args[0] {
	case "bash":
		_, err := fmt.Fprint(stdout, bashCompletionScript)
		return err
	case "zsh":
		_, err := fmt.Fprint(stdout, zshCompletionScript)
		return err
	case "fish":
		_, err := fmt.Fprint(stdout, fishCompletionScript)
		return err
	default:
		return fmt.Errorf("unsupported completion shell %q", args[0])
	}
}

func writeHumanPrime(stdout io.Writer, options inspectCommandOptions, status statusBriefing, recent []requestBriefing, commands []string) error {
	_, _ = fmt.Fprintln(stdout, "Gatelet client")
	if status.PublicURL != "" {
		_, _ = fmt.Fprintf(stdout, "Public URL: %s\n", status.PublicURL)
	}
	if status.Target != "" {
		_, _ = fmt.Fprintf(stdout, "Target: %s\n", status.Target)
	}
	if options.Addr != "" {
		_, _ = fmt.Fprintf(stdout, "Inspect API: %s\n", options.Addr)
	}
	_, _ = fmt.Fprintf(stdout, "Paused: %t\n", status.Paused)
	_, _ = fmt.Fprintf(stdout, "Queue depth: %d\n", status.QueueDepth)
	_, _ = fmt.Fprintf(stdout, "Requests: %d\n", status.RequestCount)
	_, _ = fmt.Fprintln(stdout)
	_, _ = fmt.Fprintln(stdout, "Recent requests:")
	if len(recent) == 0 {
		_, _ = fmt.Fprintln(stdout, "  none")
	} else {
		for _, request := range recent {
			statusText := "pending"
			if request.StatusCode != 0 {
				statusText = strconv.Itoa(request.StatusCode)
			}
			if request.Error != "" {
				statusText = "ERR"
			}
			_, _ = fmt.Fprintf(stdout, "  #%d %s %s %s\n", request.ID, request.Method, request.RequestURI, statusText)
		}
	}
	_, _ = fmt.Fprintln(stdout)
	_, _ = fmt.Fprintln(stdout, "Commands:")
	for _, command := range commands {
		_, _ = fmt.Fprintf(stdout, "  %s\n", command)
	}
	return nil
}

func parseInspectCommandOptions(name string, args []string, requireTrailingArg bool) (inspectCommandOptions, error) {
	options := inspectCommandOptions{Timeout: 30 * time.Second}
	if requireTrailingArg && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		options.TrailingArg = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.Addr, "addr", "", "inspect API base URL")
	flags.StringVar(&options.Token, "token", "", "inspect API token")
	flags.IntVar(&options.Limit, "limit", 0, "maximum requests to return")
	flags.Uint64Var(&options.SinceID, "since-id", 0, "only include requests after this ID")
	flags.StringVar(&options.Method, "method", "", "filter by HTTP method")
	flags.IntVar(&options.Status, "status", 0, "filter by HTTP status")
	flags.StringVar(&options.ErrorKind, "error-kind", "", "filter by error kind")
	flags.StringVar(&options.Path, "path-contains", "", "filter by request path substring")
	flags.StringVar(&options.Event, "event", "", "event type to wait for")
	flags.DurationVar(&options.Timeout, "timeout", options.Timeout, "maximum time to wait")
	flags.BoolVar(&options.JSON, "json", false, "print JSON output")
	if err := flags.Parse(args); err != nil {
		return inspectCommandOptions{}, err
	}
	remaining := flags.Args()
	if requireTrailingArg {
		if options.TrailingArg == "" && len(remaining) == 1 {
			options.TrailingArg = remaining[0]
		}
		if options.TrailingArg == "" {
			return inspectCommandOptions{}, fmt.Errorf("request id is required")
		}
		if len(remaining) > 0 && remaining[0] != options.TrailingArg {
			return inspectCommandOptions{}, fmt.Errorf("unexpected argument %q", remaining[0])
		}
	} else if len(remaining) != 0 {
		return inspectCommandOptions{}, fmt.Errorf("unexpected argument %q", remaining[0])
	}
	return options, nil
}

func applyAgentStateDefaults(options *inspectCommandOptions) error {
	if options.Addr != "" && options.Token != "" {
		return nil
	}
	state, err := readAgentState()
	if err != nil {
		if options.Addr == "" {
			return err
		}
		return nil
	}
	if options.Addr == "" {
		options.Addr = state.InspectURL
	}
	if options.Token == "" {
		options.Token = state.InspectToken
	}
	if options.Addr == "" {
		return fmt.Errorf("--addr is required")
	}
	return nil
}

func writeAgentState(config client.Config) error {
	path, err := agentStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create agent state directory: %w", err)
	}
	state := agentState{
		InspectURL:   config.InspectAddr,
		InspectToken: config.InspectToken,
		PublicURL:    client.PublicURL(config.Name, config.Domain, config.ServerAddr),
		Target:       config.Target,
		UpdatedAt:    time.Now(),
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode agent state: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write agent state: %w", err)
	}
	return nil
}

func readAgentState() (agentState, error) {
	path, err := agentStatePath()
	if err != nil {
		return agentState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agentState{}, fmt.Errorf("--addr is required and no agent state was found at %s", path)
	}
	var state agentState
	if err := json.Unmarshal(data, &state); err != nil {
		return agentState{}, fmt.Errorf("decode agent state: %w", err)
	}
	return state, nil
}

func agentStatePath() (string, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "gatelet", "agent.json"), nil
}

func writeInspectResponse(stdout io.Writer, data []byte, err error) error {
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(data, '\n'))
	return err
}

func inspectAPIRequest(ctx context.Context, method string, options inspectCommandOptions, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(options.Addr, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if options.Token != "" {
		req.Header.Set("Authorization", "Bearer "+options.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("inspect API %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func requestsPath(options inspectCommandOptions) string {
	values := url.Values{}
	if options.Limit > 0 {
		values.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.SinceID > 0 {
		values.Set("since_id", strconv.FormatUint(options.SinceID, 10))
	}
	if options.Method != "" {
		values.Set("method", options.Method)
	}
	if options.Status != 0 {
		values.Set("status", strconv.Itoa(options.Status))
	}
	if options.ErrorKind != "" {
		values.Set("error_kind", options.ErrorKind)
	}
	if options.Path != "" {
		values.Set("path_contains", options.Path)
	}
	if len(values) == 0 {
		return "/api/requests"
	}
	return "/api/requests?" + values.Encode()
}

func runInspectWait(stdout io.Writer, options inspectCommandOptions) error {
	if options.Event == "" {
		return fmt.Errorf("--event is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(options.Addr, "/")+"/api/events", nil)
	if err != nil {
		return err
	}
	if options.Token != "" {
		req.Header.Set("Authorization", "Bearer "+options.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("inspect API events returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	scanner := bufio.NewScanner(resp.Body)
	var eventType string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if eventType == options.Event && strings.HasPrefix(line, "data: ") {
			_, err := fmt.Fprintln(stdout, strings.TrimPrefix(line, "data: "))
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("event %q was not observed", options.Event)
}

func parseBasicAuthSpec(value string) (string, string, error) {
	user, password, ok := strings.Cut(value, ":")
	if !ok || user == "" || password == "" {
		return "", "", fmt.Errorf("--basic-auth/GATELET_BASIC_AUTH must be user:password")
	}
	return user, password, nil
}

func writeStartup(w io.Writer, config client.Config) {
	url := client.PublicURL(config.Name, config.Domain, config.ServerAddr)
	switch config.LogFormat {
	case client.LogFormatJSON, client.LogFormatJSONL:
		data, err := json.Marshal(startupLogRecord{
			Type:            "startup",
			URL:             url,
			Target:          config.Target,
			LogFormat:       string(config.LogFormat),
			InspectURL:      inspectURLForStartup(config),
			InspectToken:    inspectTokenForStartup(config),
			CapabilitiesURL: capabilitiesURLForStartup(config),
		})
		if err == nil {
			_, _ = fmt.Fprintln(w, string(data))
		}
	default:
		_, _ = fmt.Fprintf(w, "url %s\ntarget %s\n", url, config.Target)
	}
}

type startupLogRecord struct {
	Type            string `json:"type"`
	URL             string `json:"url"`
	Target          string `json:"target"`
	LogFormat       string `json:"log_format"`
	InspectURL      string `json:"inspect_url,omitempty"`
	InspectToken    string `json:"inspect_token,omitempty"`
	CapabilitiesURL string `json:"capabilities_url,omitempty"`
}

func inspectURLForStartup(config client.Config) string {
	if !config.Agent {
		return ""
	}
	return config.InspectAddr
}

func inspectTokenForStartup(config client.Config) string {
	if !config.Agent {
		return ""
	}
	return config.InspectToken
}

func capabilitiesURLForStartup(config client.Config) string {
	if !config.Agent || config.InspectAddr == "" {
		return ""
	}
	return strings.TrimRight(config.InspectAddr, "/") + "/api/capabilities"
}

func generateInspectToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate inspect token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

const bashCompletionScript = `# bash completion for gatelet
_gatelet_completion() {
	local cur command
	COMPREPLY=()
	cur="${COMP_WORDS[COMP_CWORD]}"
	command="${COMP_WORDS[1]}"

	local commands="prime inspect completion"
	local global_flags="--name --server --to --token --token-id --domain --log-format --preview-size --basic-auth --inspect --inspect-addr --control-plaintext --control-ca --control-server-name --control-insecure-skip-verify --tui --agent --help"
	local prime_flags="--addr --token --json --help"
	local inspect_commands="status requests request replay pause resume wait capabilities openapi"
	local inspect_flags="--addr --token --limit --since-id --method --status --error-kind --path-contains --event --help"
	local shells="bash zsh fish"

	case "$command" in
		prime)
			COMPREPLY=($(compgen -W "$prime_flags" -- "$cur"))
			;;
		inspect)
			if [[ $COMP_CWORD -eq 2 ]]; then
				COMPREPLY=($(compgen -W "$inspect_commands" -- "$cur"))
			else
				COMPREPLY=($(compgen -W "$inspect_flags" -- "$cur"))
			fi
			;;
		completion)
			COMPREPLY=($(compgen -W "$shells" -- "$cur"))
			;;
		*)
			if [[ $cur == -* ]]; then
				COMPREPLY=($(compgen -W "$global_flags" -- "$cur"))
			else
				COMPREPLY=($(compgen -W "$commands $global_flags" -- "$cur"))
			fi
			;;
	esac
}
complete -F _gatelet_completion gatelet
`

const zshCompletionScript = `#compdef gatelet

_gatelet() {
	local -a commands global_flags prime_flags inspect_commands inspect_flags shells
	commands=(prime inspect completion)
	global_flags=(--name --server --to --token --token-id --domain --log-format --preview-size --basic-auth --inspect --inspect-addr --control-plaintext --control-ca --control-server-name --control-insecure-skip-verify --tui --agent --help)
	prime_flags=(--addr --token --json --help)
	inspect_commands=(status requests request replay pause resume wait capabilities openapi)
	inspect_flags=(--addr --token --limit --since-id --method --status --error-kind --path-contains --event --help)
	shells=(bash zsh fish)

	case ${words[2]} in
		prime)
			_describe 'prime flags' prime_flags
			;;
		inspect)
			if (( CURRENT == 3 )); then
				_describe 'inspect commands' inspect_commands
			else
				_describe 'inspect flags' inspect_flags
			fi
			;;
		completion)
			_describe 'shells' shells
			;;
		*)
			_describe 'commands' commands
			_describe 'flags' global_flags
			;;
	esac
}

_gatelet "$@"
`

const fishCompletionScript = `# fish completion for gatelet
complete -c gatelet -f
complete -c gatelet -n '__fish_use_subcommand' -a 'prime inspect completion'

complete -c gatelet -l name
complete -c gatelet -l server
complete -c gatelet -l to
complete -c gatelet -l token
complete -c gatelet -l token-id
complete -c gatelet -l domain
complete -c gatelet -l log-format -a 'text json jsonl'
complete -c gatelet -l preview-size
complete -c gatelet -l basic-auth
complete -c gatelet -l inspect
complete -c gatelet -l inspect-addr
complete -c gatelet -l control-plaintext
complete -c gatelet -l control-ca
complete -c gatelet -l control-server-name
complete -c gatelet -l control-insecure-skip-verify
complete -c gatelet -l tui
complete -c gatelet -l agent
complete -c gatelet -l help

complete -c gatelet -n '__fish_seen_subcommand_from prime' -l addr
complete -c gatelet -n '__fish_seen_subcommand_from prime' -l token
complete -c gatelet -n '__fish_seen_subcommand_from prime' -l json
complete -c gatelet -n '__fish_seen_subcommand_from prime' -l help

complete -c gatelet -n '__fish_seen_subcommand_from inspect; and not __fish_seen_subcommand_from status requests request replay pause resume wait capabilities openapi' -a 'status requests request replay pause resume wait capabilities openapi'
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l addr
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l token
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l limit
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l since-id
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l method
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l status
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l error-kind
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l path-contains
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l event
complete -c gatelet -n '__fish_seen_subcommand_from inspect' -l help

complete -c gatelet -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
`

func writeUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: gatelet <name> <target> --server <addr> [flags]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Expose a local HTTP service through a gateletd relay.")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Required:")
	_, _ = fmt.Fprintln(w, "  <name>                 public tunnel name, for example alex")
	_, _ = fmt.Fprintln(w, "  <target>               local HTTP target, for example http://127.0.0.1:3000")
	_, _ = fmt.Fprintln(w, "  --server <addr>        gateletd control address or ws(s) URL, or set GATELET_SERVER")
	_, _ = fmt.Fprintln(w, "  --token <token>        shared tunnel token, or set GATELET_TOKEN")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Flags:")
	_, _ = fmt.Fprintln(w, "  --name <name>          tunnel name when not using the positional form")
	_, _ = fmt.Fprintln(w, "  --to <url>             compatibility alias for the positional target")
	_, _ = fmt.Fprintln(w, "  --token-id <id>        token ID for daemon-side rotation")
	_, _ = fmt.Fprintln(w, "  --domain <domain>      public tunnel domain shown in startup output")
	_, _ = fmt.Fprintln(w, "  --log-format <format>  request log format: text, json, or jsonl")
	_, _ = fmt.Fprintln(w, "  --preview-size <bytes> request/response body preview cap")
	_, _ = fmt.Fprintln(w, "  --basic-auth <u:p>     require HTTP Basic Auth before forwarding")
	_, _ = fmt.Fprintln(w, "  --inspect              serve a local request inspection and control API")
	_, _ = fmt.Fprintln(w, "  --inspect-addr <addr>  local inspect API address, default 127.0.0.1:0")
	_, _ = fmt.Fprintln(w, "  --agent                enable machine-readable agent mode")
	_, _ = fmt.Fprintln(w, "  --control-plaintext    disable TLS for development control connections")
	_, _ = fmt.Fprintln(w, "  --control-ca <path>    CA bundle for verifying the control server")
	_, _ = fmt.Fprintln(w, "  --control-server-name <name>")
	_, _ = fmt.Fprintln(w, "                         TLS server name override")
	_, _ = fmt.Fprintln(w, "  --control-insecure-skip-verify")
	_, _ = fmt.Fprintln(w, "                         use TLS without certificate verification")
	_, _ = fmt.Fprintln(w, "  --tui                  show the live tunnel dashboard")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Examples:")
	_, _ = fmt.Fprintln(w, "  gatelet alex http://127.0.0.1:3000 --server wss://tun.aresa.me --token \"$GATELET_TOKEN\"")
	_, _ = fmt.Fprintln(w, "  gatelet alex http://127.0.0.1:3000 --server 127.0.0.1:4443 --token dev --control-plaintext --tui")
	_, _ = fmt.Fprintln(w, "  gatelet alex http://127.0.0.1:3000 --server wss://tun.aresa.me --token \"$GATELET_TOKEN\" --agent")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Agent commands:")
	_, _ = fmt.Fprintln(w, "  gatelet prime [--addr <inspect-url>] [--token <inspect-token>]")
	_, _ = fmt.Fprintln(w, "  gatelet prime --json [--addr <inspect-url>] [--token <inspect-token>]")
	_, _ = fmt.Fprintln(w, "  gatelet inspect status|capabilities|requests|request|replay|pause|resume|wait|openapi [--addr <inspect-url>]")
	_, _ = fmt.Fprintln(w, "  gatelet completion bash|zsh|fish")
	_, _ = fmt.Fprintln(w, "  use --name prime or --name inspect for tunnels with command-word names")
}
