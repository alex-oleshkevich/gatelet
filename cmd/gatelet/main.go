package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"gatelet/internal/client"
	"gatelet/internal/protocol"
	"gatelet/internal/tui"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
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
	if err := client.Run(ctx, config); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func parseConfig(args []string) (client.Config, error) {
	var config client.Config
	logFormat := string(client.LogFormatText)
	controlPlaintext := false
	positionalTarget := ""

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
	flags.BoolVar(&controlPlaintext, "control-plaintext", controlPlaintext, "disable TLS for the control connection")
	flags.StringVar(&config.ControlCACertFile, "control-ca", "", "PEM CA bundle for verifying the control server")
	flags.StringVar(&config.ControlServerName, "control-server-name", "", "TLS server name for the control connection")
	flags.BoolVar(&config.ControlInsecureSkipVerify, "control-insecure-skip-verify", false, "skip control TLS certificate verification")
	flags.BoolVar(&config.TUI, "tui", false, "show live tunnel dashboard")
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

	return config, nil
}

func writeStartup(w io.Writer, config client.Config) {
	url := client.PublicURL(config.Name, config.Domain, config.ServerAddr)
	switch config.LogFormat {
	case client.LogFormatJSON, client.LogFormatJSONL:
		data, err := json.Marshal(startupLogRecord{
			Type:      "startup",
			URL:       url,
			Target:    config.Target,
			LogFormat: string(config.LogFormat),
		})
		if err == nil {
			_, _ = fmt.Fprintln(w, string(data))
		}
	default:
		_, _ = fmt.Fprintf(w, "url %s\ntarget %s\n", url, config.Target)
	}
}

type startupLogRecord struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Target    string `json:"target"`
	LogFormat string `json:"log_format"`
}

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
	_, _ = fmt.Fprintln(w, "  --control-plaintext    disable TLS for raw TCP development control connections")
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
}
