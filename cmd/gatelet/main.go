package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"gatelet/internal/client"
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

	config.RequestLog = os.Stdout
	if err := client.Run(ctx, config); err != nil {
		log.Fatal(err)
	}
}

func parseConfig(args []string) (client.Config, error) {
	var config client.Config

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
	flags.StringVar(&config.Domain, "domain", "", "public tunnel domain, inferred from --server when empty")
	flags.BoolVar(&config.TUI, "tui", false, "show live tunnel dashboard")
	if err := flags.Parse(args); err != nil {
		return client.Config{}, err
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
		return client.Config{}, fmt.Errorf("--token is required")
	}

	return config, nil
}
