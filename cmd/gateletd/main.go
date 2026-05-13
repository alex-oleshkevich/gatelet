package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gatelet/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	var config server.Config
	var httpAddr string
	var controlTLSCert string
	var controlTLSKey string

	flag.StringVar(&config.Domain, "domain", "", "base domain for tunnels")
	flag.StringVar(&httpAddr, "http", ":8080", "public HTTP listen address")
	flag.StringVar(&config.ControlAddr, "control", ":4443", "tunnel control listen address")
	flag.StringVar(&config.Token, "token", "", "shared tunnel authentication token")
	flag.StringVar(&controlTLSCert, "control-tls-cert", "", "control listener TLS certificate file")
	flag.StringVar(&controlTLSKey, "control-tls-key", "", "control listener TLS private key file")
	flag.Parse()

	if config.Domain == "" {
		log.Fatal("--domain is required")
	}
	if config.Token == "" {
		config.Token = os.Getenv("GATELET_TOKEN")
	}
	if config.Token == "" {
		log.Fatal("--token or GATELET_TOKEN is required")
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
