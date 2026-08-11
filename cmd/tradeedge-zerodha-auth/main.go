// Command tradeedge-zerodha-auth performs bounded, read-only Zerodha operator
// authentication checks. It never starts TradeEdge or exposes broker mutation.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

const (
	apiKeyEnvironment        = "TRADEEDGE_ZERODHA_API_KEY"
	accessTokenEnvironment   = "TRADEEDGE_ZERODHA_ACCESS_TOKEN"
	accessExpiryEnvironment  = "TRADEEDGE_ZERODHA_ACCESS_TOKEN_EXPIRES_AT"
	requestTokenEnvironment  = "TRADEEDGE_ZERODHA_REQUEST_TOKEN"
	day0WatchlistID          = "day0-index-observation/v1"
	defaultOperatorTimeout   = 10 * time.Second
	defaultObservationMaxAge = 5 * time.Second
)

var (
	errInvalidCommand        = errors.New("invalid Zerodha authentication command")
	errInvalidConfiguration  = errors.New("invalid Zerodha authentication configuration")
	errAuthentication        = errors.New("Zerodha authentication failed")
	errDiagnosticReported    = errors.New("authentication diagnostic reported")
	errRESTVerification      = errors.New("Zerodha REST verification failed")
	errWebSocketVerification = errors.New("Zerodha WebSocket verification failed")
	apiKeyPattern            = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	supportedCommands        = []string{"login-url", "exchange-token", "verify-rest", "verify-websocket"}
)

type lookupEnv func(string) (string, bool)

type commandDependencies struct {
	clock        brokerzerodha.Clock
	roundTripper http.RoundTripper
	dialer       brokerzerodha.MarketDialer
	streamConfig brokerzerodha.MarketStreamConfig
	loadTokens   func(string) ([]string, error)
}

func main() {
	if err := run(os.Args[1:], os.LookupEnv, os.Stdout, commandDependencies{}); err != nil {
		if !errors.Is(err, errDiagnosticReported) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func run(args []string, lookup lookupEnv, output io.Writer, dependencies commandDependencies) error {
	if len(args) == 0 {
		return errInvalidCommand
	}
	switch args[0] {
	case "login-url":
		return loginURL(args[1:], lookup, output)
	case "exchange-token":
		return exchangeToken(args[1:], lookup, output, dependencies)
	case "verify-rest":
		return verifyREST(args[1:], lookup, output, dependencies)
	case "verify-websocket":
		return verifyWebSocket(args[1:], lookup, output, dependencies)
	default:
		return errInvalidCommand
	}
}

func loginURL(args []string, lookup lookupEnv, output io.Writer) error {
	set := flag.NewFlagSet("login-url", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		return errInvalidConfiguration
	}
	apiKey, ok := lookup(apiKeyEnvironment)
	apiKey = strings.TrimSpace(apiKey)
	if !ok || !apiKeyPattern.MatchString(apiKey) {
		return errInvalidConfiguration
	}
	login, _ := url.Parse("https://kite.zerodha.com/connect/login")
	query := login.Query()
	query.Set("v", "3")
	query.Set("api_key", apiKey)
	login.RawQuery = query.Encode()
	_, err := fmt.Fprintln(output, login.String())
	return err
}

func exchangeToken(args []string, lookup lookupEnv, output io.Writer, dependencies commandDependencies) error {
	timeout, err := parseTimeout("exchange-token", args)
	if err != nil {
		return err
	}
	if value, ok := lookup(requestTokenEnvironment); !ok || strings.TrimSpace(value) == "" {
		return errInvalidConfiguration
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	session, exchanger, err := authenticatedSession(ctx, withoutRestoredToken(lookup), dependencies)
	if err != nil {
		if exchanger != nil {
			if failure, ok := exchanger.LastAuthenticationFailure(); ok {
				if writeErr := writeAuthenticationFailure(output, failure); writeErr != nil {
					return writeErr
				}
				return errors.Join(errAuthentication, errDiagnosticReported)
			}
		}
		return errAuthentication
	}
	defer session.Shutdown()
	snapshot := session.Snapshot()
	if snapshot.State != brokerzerodha.SessionAuthenticated || snapshot.ExpiresAt.IsZero() {
		return errAuthentication
	}
	if _, err = fmt.Fprintln(output, "AUTHENTICATED"); err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, "ACCESS_TOKEN_EXPIRES_AT="+snapshot.ExpiresAt.UTC().Format(time.RFC3339))
	return err
}

func verifyREST(args []string, lookup lookupEnv, output io.Writer, dependencies commandDependencies) error {
	timeout, err := parseTimeout("verify-rest", args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	session, _, err := authenticatedSession(ctx, lookup, dependencies)
	if err != nil {
		_, _ = fmt.Fprintln(output, "REST_AUTH=FAIL")
		return errRESTVerification
	}
	zerodhaConfig, err := loadReadOnlyConfig(lookup)
	if err != nil {
		session.Shutdown()
		_, _ = fmt.Fprintln(output, "REST_AUTH=FAIL")
		return errRESTVerification
	}
	transport, err := brokerzerodha.NewHTTPTransport(zerodhaConfig, dependencies.roundTripper)
	if err != nil {
		session.Shutdown()
		_, _ = fmt.Fprintln(output, "REST_AUTH=FAIL")
		return errRESTVerification
	}
	client, err := brokerzerodha.NewClient(transport, session, operatorClock(dependencies), nil)
	if err != nil {
		transport.CloseIdleConnections()
		session.Shutdown()
		_, _ = fmt.Fprintln(output, "REST_AUTH=FAIL")
		return errRESTVerification
	}
	defer client.Shutdown()
	if _, err = client.Capabilities(ctx); err != nil {
		_, _ = fmt.Fprintln(output, "REST_AUTH=FAIL")
		return errRESTVerification
	}
	_, err = fmt.Fprintln(output, "REST_AUTH=PASS")
	return err
}

func verifyWebSocket(args []string, lookup lookupEnv, output io.Writer, dependencies commandDependencies) error {
	set := flag.NewFlagSet("verify-websocket", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	bundlePath := set.String("runtime-bundle", "", "checksum-pinned Day-0 runtime bundle")
	timeout := set.Duration("timeout", defaultOperatorTimeout, "bounded verification timeout")
	maxAge := set.Duration("max-age", defaultObservationMaxAge, "maximum observation age")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || *timeout <= 0 || *timeout > 30*time.Second || *maxAge <= 0 || *maxAge > time.Minute {
		writeWebSocketResult(output, false, 0, false)
		return errInvalidConfiguration
	}
	if strings.TrimSpace(*bundlePath) == "" {
		*bundlePath, _ = lookup("TRADEEDGE_RUNTIME_BUNDLE")
	}
	loader := dependencies.loadTokens
	if loader == nil {
		loader = loadDay0Tokens
	}
	tokens, err := loader(strings.TrimSpace(*bundlePath))
	if err != nil {
		writeWebSocketResult(output, false, 0, false)
		return errWebSocketVerification
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	session, _, err := authenticatedSession(ctx, lookup, dependencies)
	if err != nil {
		writeWebSocketResult(output, false, 0, false)
		return errWebSocketVerification
	}
	defer session.Shutdown()
	dialer := dependencies.dialer
	if dialer == nil {
		dialer = brokerzerodha.NewWebSocketMarketDialer()
	}
	streamConfig := dependencies.streamConfig
	if streamConfig.URL == "" {
		streamConfig = brokerzerodha.DefaultMarketStreamConfig()
	}
	stream, err := brokerzerodha.NewMarketStream(streamConfig, dialer, session, operatorClock(dependencies), nil)
	if err != nil {
		writeWebSocketResult(output, false, 0, false)
		return errWebSocketVerification
	}
	seen := make(map[string]struct{}, len(tokens))
	expected := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		expected[token] = struct{}{}
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	err = stream.Stream(streamCtx, tokens, func(_ context.Context, observation marketdata.Observation) error {
		if _, ok := expected[observation.ProviderToken]; !ok || !freshObservation(observation, operatorClock(dependencies).Now(), *maxAge) {
			return errWebSocketVerification
		}
		seen[observation.ProviderToken] = struct{}{}
		if len(seen) == len(expected) {
			stopStream()
		}
		return nil
	})
	stopStream()
	stream.Shutdown()
	shutdown := stream.Snapshot().State == brokerzerodha.StreamStopped
	passed := len(seen) == len(expected) && shutdown && (err == nil || errors.Is(err, context.Canceled))
	writeWebSocketResult(output, passed, len(seen), shutdown)
	if !passed {
		return errWebSocketVerification
	}
	return nil
}

func authenticatedSession(ctx context.Context, lookup lookupEnv, dependencies commandDependencies) (*brokerzerodha.SessionManager, *brokerzerodha.HTTPTokenExchanger, error) {
	zerodhaConfig, err := loadReadOnlyConfig(lookup)
	if err != nil {
		return nil, nil, err
	}
	credentials, err := (brokerzerodha.EnvCredentialSource{Lookup: brokerzerodha.LookupEnv(lookup)}).Load(ctx)
	if err != nil {
		return nil, nil, err
	}
	exchanger, err := brokerzerodha.NewHTTPTokenExchanger(zerodhaConfig, dependencies.roundTripper, operatorClock(dependencies))
	if err != nil {
		return nil, nil, err
	}
	session := brokerzerodha.NewSessionManager(credentials, exchanger, operatorClock(dependencies), nil)
	if err = session.Authenticate(ctx); err != nil {
		session.Shutdown()
		return nil, exchanger, err
	}
	return session, exchanger, nil
}

func writeAuthenticationFailure(output io.Writer, failure brokerzerodha.AuthenticationFailure) error {
	_, err := fmt.Fprintf(output, "AUTHENTICATION=FAIL\nERROR_TYPE=%s\nMESSAGE=%s\nHTTP_STATUS=%d\n", failure.ErrorType, failure.Message, failure.HTTPStatus)
	return err
}

func loadReadOnlyConfig(lookup lookupEnv) (brokerzerodha.Config, error) {
	value, err := brokerzerodha.LoadConfig(brokerzerodha.LookupEnv(lookup))
	if err != nil || !value.Enabled {
		return brokerzerodha.Config{}, errInvalidConfiguration
	}
	return value, nil
}

func loadDay0Tokens(path string) ([]string, error) {
	if path == "" {
		return nil, errInvalidConfiguration
	}
	bundle, err := config.LoadRuntimeBundle(path)
	if err != nil || bundle.Watchlist.ID != day0WatchlistID || len(bundle.Watchlist.Requirements) != 2 || len(bundle.Tokens) != 2 {
		return nil, errInvalidConfiguration
	}
	wanted := map[string]struct{}{"NIFTY 50": {}, "NIFTY BANK": {}}
	for _, requirement := range bundle.Watchlist.Requirements {
		instrument, found := bundle.Master.Instrument(requirement.InstrumentID)
		if !found || requirement.Provider != domain.Provider("zerodha") || requirement.Exchange != domain.ExchangeNSE || requirement.Segment != domain.SegmentIndex || requirement.EventKind != marketmodel.EventKindQuote || !requirement.Required {
			return nil, errInvalidConfiguration
		}
		if _, ok := wanted[instrument.Symbol()]; !ok {
			return nil, errInvalidConfiguration
		}
		delete(wanted, instrument.Symbol())
	}
	if len(wanted) != 0 {
		return nil, errInvalidConfiguration
	}
	result := append([]string(nil), bundle.Tokens...)
	sort.Strings(result)
	return result, nil
}

func parseTimeout(name string, args []string) (time.Duration, error) {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	timeout := set.Duration("timeout", defaultOperatorTimeout, "bounded operation timeout")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || *timeout <= 0 || *timeout > 30*time.Second {
		return 0, errInvalidConfiguration
	}
	return *timeout, nil
}

func operatorClock(dependencies commandDependencies) brokerzerodha.Clock {
	if dependencies.clock != nil {
		return dependencies.clock
	}
	return brokerzerodha.RealClock{}
}

func withoutRestoredToken(lookup lookupEnv) lookupEnv {
	return func(key string) (string, bool) {
		if key == accessTokenEnvironment || key == accessExpiryEnvironment {
			return "", false
		}
		return lookup(key)
	}
}

func freshObservation(value marketdata.Observation, now time.Time, maxAge time.Duration) bool {
	if value.ExchangeTime.IsZero() || value.IngestedAt.IsZero() {
		return false
	}
	age := now.Sub(value.ExchangeTime)
	lag := value.IngestedAt.Sub(value.ExchangeTime)
	return age >= -time.Second && age <= maxAge && lag >= -time.Second && lag <= maxAge
}

func writeWebSocketResult(output io.Writer, passed bool, observations int, shutdown bool) {
	auth, stopped := "FAIL", "FAIL"
	if passed {
		auth = "PASS"
	}
	if shutdown {
		stopped = "PASS"
	}
	_, _ = fmt.Fprintf(output, "WEBSOCKET_AUTH=%s\nOBSERVATIONS_RECEIVED=%d\nSHUTDOWN=%s\n", auth, observations, stopped)
}
