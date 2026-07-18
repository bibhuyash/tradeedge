package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/platform/logging"
)

type shutdownRecorder struct {
	called chan struct{}
}

func (recorder shutdownRecorder) Shutdown(context.Context) error {
	close(recorder.called)
	return nil
}

func TestRunStopsGracefullyWhenContextIsCancelled(t *testing.T) {
	logger, err := logging.New("error", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("logging.New() error = %v", err)
	}
	cfg := config.Config{
		Environment:            "test",
		HTTPAddress:            "127.0.0.1:0",
		LogLevel:               "error",
		ShutdownTimeout:        time.Second,
		TradingMode:            config.ModePaper,
		StrategyMaxConcurrency: 4,
		StrategyTimeout:        100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, cfg, logger)
	}()

	time.Sleep(25 * time.Millisecond)
	shutdownStarted := time.Now()
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		elapsed := time.Since(shutdownStarted)
		if elapsed > cfg.ShutdownTimeout+500*time.Millisecond {
			t.Fatalf("shutdown took %s, limit is %s",
				elapsed, cfg.ShutdownTimeout+500*time.Millisecond)
		}
		t.Logf("graceful shutdown completed in %s", elapsed)
	case <-time.After(cfg.ShutdownTimeout + 500*time.Millisecond):
		t.Fatal("Run() did not stop within deadline")
	}
}

func TestRunRejectsLiveModeBeforeStartup(t *testing.T) {
	logger, err := logging.New("error", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("logging.New() error = %v", err)
	}
	cfg := config.Config{
		Environment:            "test",
		HTTPAddress:            "127.0.0.1:0",
		LogLevel:               "error",
		ShutdownTimeout:        time.Second,
		TradingMode:            "live",
		StrategyMaxConcurrency: 4,
		StrategyTimeout:        100 * time.Millisecond,
	}
	err = Run(context.Background(), cfg, logger)
	if err == nil || !strings.Contains(err.Error(), "live trading is unavailable") {
		t.Fatalf("Run() error = %v, want live-mode rejection", err)
	}
}

func TestRunShutsDownInjectedStrategyRunner(t *testing.T) {
	logger, _ := logging.New("error", &bytes.Buffer{})
	cfg := config.Config{
		Environment: "test", HTTPAddress: "127.0.0.1:0", LogLevel: "error",
		ShutdownTimeout: time.Second, TradingMode: config.ModePaper,
		StrategyMaxConcurrency: 4, StrategyTimeout: 100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	called := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- RunWithOptions(ctx, cfg, logger, Options{
			StrategyRunner: shutdownRecorder{called: called},
		})
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("strategy runner shutdown was not called")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}
