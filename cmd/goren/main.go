package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorenx/goren/internal/server"
	"github.com/gorenx/goren/settings/local"
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errArguments) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	options, err := parseConfig(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(os.Stdout, commandHelp)
		return nil
	}
	if err != nil {
		return err
	}
	input, err := local.Load(options.path, options.overrides, os.LookupEnv)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	configured, err := server.ConfigFromLocal(input, cwd, options.version)
	if err != nil {
		return err
	}
	lifetime, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := configured.Inspect(lifetime, processEnvironment{}); err != nil {
		return err
	}
	if options.check {
		fmt.Fprintln(os.Stdout, "Configuration is valid.")
		return nil
	}
	process, err := server.New(configured, processEnvironment{})
	if err != nil {
		return err
	}
	err = process.Start(lifetime)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return errors.Join(err, process.Close(cleanup))
	}
	fmt.Fprintf(os.Stdout, "Goren Agent started\n  Web: http://%s\n  Workspace: %s\n  Data: %s\n", process.Address(), cwd, *input.Storage.DataDirectory)
	var failure error
	select {
	case <-lifetime.Done():
	case failure = <-process.Done():
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(failure, process.Close(cleanup))
}

type processEnvironment struct{}

func (processEnvironment) Lookup(name string) (string, bool) { return os.LookupEnv(name) }
func (processEnvironment) UserHomeDir() (string, error)      { return os.UserHomeDir() }
