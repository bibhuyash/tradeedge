package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
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
		RiskMaxConcurrency:     4,
		RiskTimeout:            100 * time.Millisecond,
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
		RiskMaxConcurrency:     4,
		RiskTimeout:            100 * time.Millisecond,
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
		RiskMaxConcurrency: 4, RiskTimeout: 100 * time.Millisecond,
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

func TestDefaultRuntimeStartsWithoutStrategyInstances(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	logger, _ := logging.New("error", &bytes.Buffer{})
	cfg := config.Config{
		Environment: "test", HTTPAddress: address, LogLevel: "error",
		ShutdownTimeout: time.Second, TradingMode: config.ModePaper,
		StrategyMaxConcurrency: 4, StrategyTimeout: 100 * time.Millisecond,
		RiskMaxConcurrency: 4, RiskTimeout: 100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, cfg, logger)
	}()
	defer func() {
		cancel()
		if err := <-result; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	}()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	var response *http.Response
	for attempt := 0; attempt < 25; attempt++ {
		response, err = client.Get("http://" + address + "/api/v1/strategy/instances")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("query strategy instances: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 0 {
		t.Fatalf("default runtime started with %d strategy instances", len(body.Items))
	}
}
