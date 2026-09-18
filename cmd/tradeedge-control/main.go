package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bibhuyash/tradeedge/internal/controlplane"
	"github.com/bibhuyash/tradeedge/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	config, err := controlplane.LoadConfig(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load control-plane configuration: %w", err)
	}
	logger, err := logging.New("info", os.Stdout)
	if err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := controlplane.New(ctx, config, controlplane.Dependencies{})
	if err != nil {
		return err
	}
	return application.Run(ctx, logger)
}
