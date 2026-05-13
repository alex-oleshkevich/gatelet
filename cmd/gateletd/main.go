package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gatelet/internal/protocol"
	"gatelet/internal/server"
)

func main() {
	logFormat := "text"
	logger, err := loggerForFormat(logFormat, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	slog.SetDefault(logger)

	var config server.Config
	var httpAddr string
	var controlTLSCert string
	var controlTLSKey string
	var tokensSpec string
	var reservedNamesSpec string
	var allowNamesSpec string

	flag.StringVar(&config.Domain, "domain", "", "base domain for tunnels")
	flag.StringVar(&httpAddr, "http", ":8080", "public HTTP listen address")
	flag.StringVar(&config.ControlAddr, "control", ":4443", "tunnel control listen address")
	flag.StringVar(&config.Token, "token", "", "shared tunnel authentication token")
	flag.StringVar(&tokensSpec, "tokens", "", "comma-separated token specs: id=value or id=value:inactive")
	flag.StringVar(&reservedNamesSpec, "reserved-names", "", "comma-separated extra reserved tunnel names")
	flag.StringVar(&allowNamesSpec, "allow-names", "", "comma-separated allowed tunnel names; empty allows any non-reserved name")
	flag.StringVar(&logFormat, "log-format", logFormat, "daemon log format: text or json")
	flag.StringVar(&controlTLSCert, "control-tls-cert", "", "control listener TLS certificate file")
	flag.StringVar(&controlTLSKey, "control-tls-key", "", "control listener TLS private key file")
	flag.Parse()
	logger, err = loggerForFormat(logFormat, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	slog.SetDefault(logger)

	if config.Domain == "" {
		log.Fatal("--domain is required")
	}
	if config.Token == "" {
		config.Token = os.Getenv("GATELET_TOKEN")
	}
	if tokensSpec == "" {
		tokensSpec = os.Getenv("GATELET_TOKENS")
	}
	if tokensSpec != "" {
		tokens, err := parseTokenSpecs(tokensSpec)
		if err != nil {
			log.Fatalf("parse tokens: %v", err)
		}
		config.Tokens = tokens
	}
	if reservedNamesSpec == "" {
		reservedNamesSpec = os.Getenv("GATELET_RESERVED_NAMES")
	}
	if reservedNamesSpec != "" {
		names, err := parseNameList(reservedNamesSpec)
		if err != nil {
			log.Fatalf("parse reserved names: %v", err)
		}
		config.ReservedNames = names
	}
	if allowNamesSpec == "" {
		allowNamesSpec = os.Getenv("GATELET_ALLOW_NAMES")
	}
	if allowNamesSpec != "" {
		names, err := parseNameList(allowNamesSpec)
		if err != nil {
			log.Fatalf("parse allow names: %v", err)
		}
		config.AllowNames = names
	}
	if config.Token == "" && len(config.Tokens) == 0 {
		log.Fatal("--token/GATELET_TOKEN or --tokens/GATELET_TOKENS is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	config.Logger = logger
	relay := server.New(config)

	control, err := net.Listen("tcp", config.ControlAddr)
	if err != nil {
		log.Fatalf("listen control: %v", err)
	}
	controlTLS := false
	if controlTLSCert != "" || controlTLSKey != "" {
		if controlTLSCert == "" || controlTLSKey == "" {
			log.Fatal("--control-tls-cert and --control-tls-key must be provided together")
		}
		cert, err := tls.LoadX509KeyPair(controlTLSCert, controlTLSKey)
		if err != nil {
			log.Fatalf("load control TLS certificate: %v", err)
		}
		control = tls.NewListener(control, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
		controlTLS = true
	}

	go func() {
		if err := relay.ServeControl(ctx, control); err != nil {
			log.Printf("control server stopped: %v", err)
			stop()
		}
	}()

	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           relay,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}

	go func() {
		<-ctx.Done()
		_ = httpServer.Shutdown(context.Background())
	}()

	logger.Info("gateletd listening", "http", httpAddr, "control", control.Addr().String(), "control_tls", controlTLS, "domain", config.Domain)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server: %v", err)
	}
}

func loggerForFormat(format string, w io.Writer) (*slog.Logger, error) {
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, nil)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, nil)), nil
	default:
		return nil, fmt.Errorf("unknown --log-format %q", format)
	}
}

func parseNameList(spec string) ([]string, error) {
	var names []string
	for _, rawPart := range strings.Split(spec, ",") {
		name := strings.TrimSpace(rawPart)
		if name == "" {
			continue
		}
		if err := protocol.ValidateName(name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

func parseTokenSpecs(spec string) ([]server.Token, error) {
	var tokens []server.Token
	for _, rawPart := range strings.Split(spec, ",") {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			continue
		}
		id, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("token spec %q must use id=value", part)
		}
		id = strings.TrimSpace(id)
		value = strings.TrimSpace(value)
		if id == "" {
			return nil, fmt.Errorf("token spec %q has empty id", part)
		}
		if err := protocol.ValidateName(id); err != nil {
			return nil, fmt.Errorf("token spec %q has invalid id: %w", part, err)
		}
		if value == "" {
			return nil, fmt.Errorf("token spec %q has empty value", part)
		}

		active := true
		switch {
		case strings.HasSuffix(value, ":active"):
			value = strings.TrimSuffix(value, ":active")
		case strings.HasSuffix(value, ":inactive"):
			value = strings.TrimSuffix(value, ":inactive")
			active = false
		case strings.Contains(value, ":"):
			return nil, fmt.Errorf("token spec %q has unknown status suffix", part)
		}
		if value == "" {
			return nil, fmt.Errorf("token spec %q has empty value", part)
		}

		tokens = append(tokens, server.Token{
			ID:     id,
			Value:  value,
			Active: active,
		})
	}
	return tokens, nil
}
