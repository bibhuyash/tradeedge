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

func TestRunStopsGracefullyWhenContextIsCancelled(t *testing.T) {
	logger, err := logging.New("error", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("logging.New() error = %v", err)
	}
	cfg := config.Config{
		Environment:     "test",
		HTTPAddress:     "127.0.0.1:0",
		LogLevel:        "error",
		ShutdownTimeout: time.Second,
		TradingMode:     config.ModePaper,
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
		Environment:     "test",
		HTTPAddress:     "127.0.0.1:0",
		LogLevel:        "error",
		ShutdownTimeout: time.Second,
		TradingMode:     "live",
	}
	err = Run(context.Background(), cfg, logger)
	if err == nil || !strings.Contains(err.Error(), "live trading is unavailable") {
		t.Fatalf("Run() error = %v, want live-mode rejection", err)
	}
}
