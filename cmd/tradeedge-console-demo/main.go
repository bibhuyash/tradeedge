// Command tradeedge-console-demo serves mock presentation data on loopback only.
// It never constructs the trading application or loads its environment configuration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/bibhuyash/tradeedge/internal/operatorconsole"
)

func main() {
	address := flag.String("address", "127.0.0.1:8090", "literal loopback address for offline mock console")
	flag.Parse()
	if err := serve(*address); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}
func serve(address string) error {
	if !loopbackAddress(address) {
		return errors.New("mock console requires a literal loopback address")
	}
	server := &http.Server{Addr: address, Handler: operatorconsole.NewMock(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("MOCK DATA ONLY — http://%s/console/\n", listener.Addr())
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
