package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorenx/goren/internal/assembly"
	"github.com/gorenx/goren/plugin"
)

type commandConfig struct {
	address string
	version string
}

func main() {
	settings := parseConfig()
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve working directory:", err)
		os.Exit(1)
	}

	registry, err := assembly.NewCatalog(assembly.Environment{WorkingDirectory: workingDirectory})
	if err != nil {
		fmt.Fprintln(os.Stderr, "create factory catalog:", err)
		os.Exit(1)
	}
	declarations, err := assembly.DefaultSpecs(settings.address, settings.version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create server composition:", err)
		os.Exit(1)
	}

	lifecycle, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	engine := plugin.NewRuntime()
	if _, err := assembly.Load(lifecycle, engine, registry, declarations); err != nil {
		fmt.Fprintln(os.Stderr, "start server composition:", err)
		os.Exit(1)
	}
	<-lifecycle.Done()
	closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := engine.Shutdown(closeContext); err != nil {
		fmt.Fprintln(os.Stderr, "stop server composition:", err)
		os.Exit(1)
	}
}

func parseConfig() commandConfig {
	address := flag.String("listen", "127.0.0.1:3080", "Echo listen address")
	version := flag.String("version", "dev", "host.describe version")
	flag.Parse()
	return commandConfig{address: *address, version: *version}
}
