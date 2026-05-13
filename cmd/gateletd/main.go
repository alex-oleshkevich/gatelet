package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"gatelet/internal/server"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	var config server.Config
	var httpAddr string

	flag.StringVar(&config.Domain, "domain", "", "base domain for tunnels")
	flag.StringVar(&httpAddr, "http", ":8080", "public HTTP listen address")
	flag.StringVar(&config.ControlAddr, "control", ":4443", "tunnel control listen address")
	flag.StringVar(&config.Token, "token", "", "shared tunnel authentication token")
	flag.Parse()

	if config.Domain == "" {
		log.Fatal("--domain is required")
	}
	if config.Token == "" {
		log.Fatal("--token is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	config.Logger = logger
	relay := server.New(config)

	control, err := net.Listen("tcp", config.ControlAddr)
	if err != nil {
		log.Fatalf("listen control: %v", err)
	}

	go func() {
		if err := relay.ServeControl(ctx, control); err != nil {
			log.Printf("control server stopped: %v", err)
			stop()
		}
	}()

	httpServer := &http.Server{
		Addr:    httpAddr,
		Handler: relay,
	}

	go func() {
		<-ctx.Done()
		_ = httpServer.Shutdown(context.Background())
	}()

	logger.Info("gateletd listening", "http", httpAddr, "control", control.Addr().String(), "domain", config.Domain)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server: %v", err)
	}
}
