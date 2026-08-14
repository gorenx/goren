package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gorenx/goren/apiproxy"
	connectionhost "github.com/gorenx/goren/internal/connection"
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

	methods := apiproxy.NewCatalog()
	source := apiproxy.HostDescriptionFunc(func(context.Context) (apiproxy.HostDescription, error) {
		return apiproxy.HostDescription{
			Version: settings.version, CWD: workingDirectory,
			AttachedSessions: 0, CanOpenPath: false,
		}, nil
	})
	if err := apiproxy.RegisterHostDescribe(methods, source); err != nil {
		fmt.Fprintln(os.Stderr, "register host.describe:", err)
		os.Exit(1)
	}
	idleMuxStream := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.MuxFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	idleHostStream := func(requestContext context.Context, _ func(apiproxy.StreamRequest[apiproxy.HostFrame]) error) error {
		<-requestContext.Done()
		return nil
	}
	eventStreams, err := apiproxy.NewEventStreams(idleMuxStream, idleHostStream)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create API event streams:", err)
		os.Exit(1)
	}

	carrier, err := connectionhost.NewHTTPHost(connectionhost.HTTPConfig{}, methods, eventStreams)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create connection host:", err)
		os.Exit(1)
	}
	lifecycle, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := carrier.Start(lifecycle, settings.address); err != nil {
		fmt.Fprintln(os.Stderr, "serve connection host:", err)
		os.Exit(1)
	}
}

func parseConfig() commandConfig {
	address := flag.String("listen", "127.0.0.1:3080", "Echo listen address")
	version := flag.String("version", "dev", "host.describe version")
	flag.Parse()
	return commandConfig{address: *address, version: *version}
}
