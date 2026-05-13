package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"gatelet/internal/client"
	"gatelet/internal/protocol"
	"gatelet/internal/tui"
)

func main() {
	config, err := parseConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if config.TUI {
		if err := tui.Run(ctx, config); err != nil {
			log.Fatal(err)
		}
		return
	}

	writeStartup(os.Stdout, config)
	config.RequestLog = os.Stdout
	if err := client.Run(ctx, config); err != nil {
		log.Fatal(err)
	}
}

func parseConfig(args []string) (client.Config, error) {
	var config client.Config
	logFormat := string(client.LogFormatText)
	controlPlaintext := false

	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		config.Name = args[0]
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

	if config.Name == "" && flags.NArg() > 0 {
		config.Name = flags.Arg(0)
	}
	if config.Name == "" {
		return client.Config{}, fmt.Errorf("--name or positional name is required")
	}
	if config.ServerAddr == "" {
		return client.Config{}, fmt.Errorf("--server is required")
	}
	if config.Target == "" {
		return client.Config{}, fmt.Errorf("--to is required")
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
