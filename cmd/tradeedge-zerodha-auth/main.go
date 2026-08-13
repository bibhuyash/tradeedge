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
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketvalidation"
)

const (
	apiKeyEnvironment        = "TRADEEDGE_ZERODHA_API_KEY"
	apiSecretEnvironment     = "TRADEEDGE_ZERODHA_API_SECRET"
	accessTokenEnvironment   = "TRADEEDGE_ZERODHA_ACCESS_TOKEN"
	accessExpiryEnvironment  = "TRADEEDGE_ZERODHA_ACCESS_TOKEN_EXPIRES_AT"
	requestTokenEnvironment  = "TRADEEDGE_ZERODHA_REQUEST_TOKEN"
	readOnlyEnvironment      = "TRADEEDGE_ZERODHA_READ_ONLY"
	day0WatchlistID          = "day0-index-observation/v1"
	shadowWatchlistID        = "phase8-m4-live-shadow/v1"
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
	supportedCommands        = []string{"login-url", "exchange-token", "verify-rest", "verify-websocket", "preflight"}
)

type lookupEnv func(string) (string, bool)

type commandDependencies struct {
	clock              brokerzerodha.Clock
	roundTripper       http.RoundTripper
	dialer             brokerzerodha.MarketDialer
	streamConfig       brokerzerodha.MarketStreamConfig
	loadTokens         func(string) ([]string, error)
	loadBundleChecksum func(string) (string, error)
	currentCommit      func() (string, error)
	publishEvidence    func(string, []byte) (string, error)
}

func main() {
	os.Exit(execute(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr, commandDependencies{}))
}

func execute(args []string, lookup lookupEnv, output, errorOutput io.Writer, dependencies commandDependencies) int {
	if err := run(args, lookup, output, dependencies); err != nil {
		if !errors.Is(err, errDiagnosticReported) {
			_, _ = fmt.Fprintln(errorOutput, err)
		}
		return 1
	}
	return 0
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
	case "preflight":
		return preflight(args[1:], lookup, output, dependencies)
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
	if failure, failed := exchangePreflightFailure(lookup); failed {
		if writeErr := writeAuthenticationFailure(output, failure); writeErr != nil {
			return writeErr
		}
		return errors.Join(errAuthentication, errDiagnosticReported)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	session, err := authenticatedSession(ctx, withoutRestoredToken(lookup), dependencies)
	if err != nil {
		var failure brokerzerodha.AuthenticationFailure
		if errors.As(err, &failure) {
			if writeErr := writeAuthenticationFailure(output, failure); writeErr != nil {
				return writeErr
			}
			return errors.Join(errAuthentication, errDiagnosticReported)
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
	session, err := authenticatedSession(ctx, lookup, dependencies)
	if err != nil {
		_, _ = fmt.Fprintln(output, "REST_AUTH=FAIL")
		return errRESTVerification
	}
	client, err := readOnlyClient(lookup, session, dependencies)
	if err != nil {
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
	options, err := parseStreamOptions("verify-websocket", args, lookup)
	if err != nil {
		writeWebSocketResult(output, false, 0, false)
		return errInvalidConfiguration
	}
	loader := dependencies.loadTokens
	var tokens []string
	if loader == nil {
		tokens, err = loadPreflightTokens(options.bundlePath, options.mode)
	} else {
		tokens, err = loader(options.bundlePath)
	}
	if err != nil {
		writeWebSocketResult(output, false, 0, false)
		return errWebSocketVerification
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()
	session, err := authenticatedSession(ctx, lookup, dependencies)
	if err != nil {
		writeWebSocketResult(output, false, 0, false)
		return errWebSocketVerification
	}
	defer session.Shutdown()
	result := verifyWebSocketSession(ctx, tokens, options.maxAge, session, dependencies)
	writeWebSocketResult(output, result.passed, result.observations, result.shutdown)
	if !result.passed {
		return errWebSocketVerification
	}
	return nil
}

type streamOptions struct {
	bundlePath      string
	timeout         time.Duration
	maxAge          time.Duration
	outputPath      string
	tradingDate     string
	credentialsFile string
	mode            string
}

type webSocketResult struct {
	passed              bool
	observations        int
	shutdown            bool
	errorType           string
	message             string
	handshake           bool
	subscribeSent       bool
	expectedTokenCount  int
	expectedTokensValid bool
	binaryFrames        uint64
	heartbeats          uint64
	packets             uint64
	indexPackets        uint64
	packetsDecoded      uint64
	packetsRejected     uint64
	tokenMatches        uint64
	freshObservations   uint64
	textMessages        uint64
	brokerMessages      uint64
	instrumentMetadata  uint64
	appCodeMessages     uint64
	orderUpdates        uint64
	providerErrors      uint64
	lastFailureStage    string
	frameDiagnostics    []brokerzerodha.FrameDiagnostic
}

func parseStreamOptions(name string, args []string, lookup lookupEnv) (streamOptions, error) {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	bundlePath := set.String("runtime-bundle", "", "checksum-pinned Day-0 runtime bundle")
	timeout := set.Duration("timeout", defaultOperatorTimeout, "bounded verification timeout")
	maxAge := set.Duration("max-age", defaultObservationMaxAge, "maximum observation age")
	outputPath := set.String("output", "", "create-once safe preflight evidence JSON")
	tradingDate := set.String("date", "", "target trading date YYYY-MM-DD")
	credentialsFile := set.String("credentials-file", "", "untracked dotenv file for atomic restored-session persistence")
	mode := set.String("mode", "", "preflight mode PAPER or SHADOW")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || *timeout <= 0 || *timeout > 30*time.Second || *maxAge <= 0 || *maxAge > time.Minute {
		return streamOptions{}, errInvalidConfiguration
	}
	if name != "preflight" && (*outputPath != "" || *tradingDate != "" || *credentialsFile != "" || *mode != "") {
		return streamOptions{}, errInvalidConfiguration
	}
	if (*outputPath == "") != (*tradingDate == "") {
		return streamOptions{}, errInvalidConfiguration
	}
	if *tradingDate != "" {
		if _, err := time.Parse("2006-01-02", *tradingDate); err != nil {
			return streamOptions{}, errInvalidConfiguration
		}
	}
	if strings.TrimSpace(*credentialsFile) == "" {
		*credentialsFile, _ = lookup("TRADEEDGE_ZERODHA_CREDENTIALS_FILE")
	}
	if *outputPath != "" && strings.TrimSpace(*credentialsFile) == "" {
		return streamOptions{}, errInvalidConfiguration
	}
	if strings.TrimSpace(*mode) == "" {
		*mode, _ = lookup("TRADEEDGE_ZERODHA_MODE")
	}
	*mode = strings.ToUpper(strings.TrimSpace(*mode))
	if *mode == "" {
		*mode = "PAPER"
	}
	if *mode != "PAPER" && *mode != "SHADOW" {
		return streamOptions{}, errInvalidConfiguration
	}
	if strings.TrimSpace(*bundlePath) == "" {
		*bundlePath, _ = lookup("TRADEEDGE_RUNTIME_BUNDLE")
	}
	return streamOptions{bundlePath: strings.TrimSpace(*bundlePath), timeout: *timeout, maxAge: *maxAge, outputPath: strings.TrimSpace(*outputPath), tradingDate: *tradingDate, credentialsFile: strings.TrimSpace(*credentialsFile), mode: *mode}, nil
}

func verifyWebSocketSession(ctx context.Context, tokens []string, maxAge time.Duration, session *brokerzerodha.SessionManager, dependencies commandDependencies) webSocketResult {
	result := webSocketResult{expectedTokenCount: len(tokens), expectedTokensValid: validExpectedTokens(tokens)}
	if !result.expectedTokensValid {
		result.errorType, result.message, result.lastFailureStage = "WebSocketConfigurationError", "Invalid expected provider tokens", "EXPECTED_TOKENS"
		return result
	}
	dialer := dependencies.dialer
	if dialer == nil {
		dialer = brokerzerodha.NewWebSocketMarketDialer()
	}
	streamConfig := dependencies.streamConfig
	if streamConfig.URL == "" {
		streamConfig = brokerzerodha.DefaultMarketStreamConfig()
	}
	streamConfig.OrderTextPolicy = brokerzerodha.OrderTextObserveOnly
	stream, err := brokerzerodha.NewMarketStream(streamConfig, dialer, session, operatorClock(dependencies), nil)
	if err != nil {
		result.errorType, result.message, result.lastFailureStage = "WebSocketConfigurationError", "Invalid read-only WebSocket configuration", "HANDSHAKE"
		return result
	}
	seen := make(map[string]struct{}, len(tokens))
	expected := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		expected[token] = struct{}{}
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	err = stream.Stream(streamCtx, tokens, func(_ context.Context, observation marketdata.Observation) error {
		if _, ok := expected[observation.ProviderToken]; !ok {
			return errWebSocketVerification
		}
		if !freshObservation(observation, operatorClock(dependencies).Now(), maxAge) {
			return nil
		}
		seen[observation.ProviderToken] = struct{}{}
		if len(seen) == len(expected) {
			stopStream()
		}
		return nil
	})
	stopStream()
	stream.Shutdown()
	snapshot := stream.Snapshot()
	result.handshake = snapshot.HandshakeEstablished
	result.subscribeSent = snapshot.SubscribeSent
	result.binaryFrames = snapshot.BinaryFrames
	result.heartbeats = snapshot.Heartbeats
	result.packets = snapshot.Packets
	result.indexPackets = snapshot.IndexPackets
	result.packetsDecoded = snapshot.PacketsDecoded
	result.packetsRejected = snapshot.PacketsRejected
	result.tokenMatches = snapshot.TokenMatches
	result.textMessages = snapshot.TextMessages
	result.brokerMessages = snapshot.BrokerMessages
	result.instrumentMetadata = snapshot.InstrumentMetadata
	result.appCodeMessages = snapshot.AppCodeMessages
	result.orderUpdates = snapshot.OrderUpdates
	result.providerErrors = snapshot.ProviderErrors
	result.frameDiagnostics = append([]brokerzerodha.FrameDiagnostic(nil), snapshot.FrameDiagnostics...)
	result.freshObservations = uint64(len(seen))
	result.observations = len(seen)
	result.shutdown = snapshot.State == brokerzerodha.StreamStopped
	result.passed = len(seen) == len(expected) && result.shutdown && (err == nil || errors.Is(err, context.Canceled))
	result.errorType, result.message = classifyWebSocketFailure(err)
	result.lastFailureStage = webSocketFailureStage(result)
	return result
}

func validExpectedTokens(tokens []string) bool {
	if len(tokens) != 2 && len(tokens) != 14 {
		return false
	}
	seen := make(map[uint64]struct{}, len(tokens))
	for _, token := range tokens {
		value, err := strconv.ParseUint(strings.TrimSpace(token), 10, 32)
		if err != nil || value == 0 {
			return false
		}
		seen[value] = struct{}{}
	}
	return len(seen) == len(tokens)
}

func webSocketFailureStage(result webSocketResult) string {
	switch {
	case result.passed:
		return "NONE"
	case !result.expectedTokensValid:
		return "EXPECTED_TOKENS"
	case !result.handshake:
		return "HANDSHAKE"
	case !result.subscribeSent:
		return "SUBSCRIPTION"
	case result.binaryFrames == 0:
		if len(result.frameDiagnostics) > 0 {
			last := result.frameDiagnostics[len(result.frameDiagnostics)-1]
			if last.MessageType == brokerzerodha.MarketMessageClose {
				return "CLOSE"
			}
			return "MESSAGE_TYPE"
		}
		return "FRAME_RECEIVE"
	case result.packets == 0:
		if result.heartbeats > 0 {
			return "FRAME_RECEIVE"
		}
		return "BINARY_ENVELOPE"
	case result.packetsDecoded == 0 && result.packetsRejected > 0:
		return "PACKET_DECODE"
	case result.tokenMatches == 0:
		return "TOKEN_MATCH"
	case result.freshObservations < uint64(result.expectedTokenCount):
		return "FRESHNESS"
	default:
		return "UNKNOWN"
	}
}

func classifyWebSocketFailure(err error) (string, string) {
	switch {
	case err == nil:
		return "WebSocketVerificationError", "Read-only WebSocket verification failed"
	case errors.Is(err, context.DeadlineExceeded):
		return "WebSocketTimeout", "Timed out waiting for fresh market observations"
	case errors.Is(err, errWebSocketVerification):
		return "ObservationValidationError", "Received an unexpected or stale market observation"
	case errors.Is(err, brokerzerodha.ErrProviderTextMessage):
		return "WebSocketProviderError", "Zerodha reported a WebSocket provider error"
	case errors.Is(err, brokerzerodha.ErrMalformedTextMessage):
		return "WebSocketTextProtocolError", "Received malformed WebSocket text JSON"
	case errors.Is(err, brokerzerodha.ErrUnknownTextMessage):
		return "WebSocketMessageTypeError", "Received an unknown WebSocket text message type"
	case errors.Is(err, brokerzerodha.ErrMarketStreamMalformed):
		return "WebSocketProtocolError", "Received a malformed market-data frame"
	case errors.Is(err, brokerzerodha.ErrMarketStreamStale):
		return "WebSocketTimeout", "Market stream became stale"
	case errors.Is(err, brokerzerodha.ErrMarketStreamOverflow):
		return "WebSocketOverflow", "Market stream buffer capacity was exceeded"
	case errors.Is(err, brokerzerodha.ErrUnexpectedOrderUpdate):
		return "WebSocketSafetyError", "Unexpected order update received on read-only stream"
	case errors.Is(err, brokerzerodha.ErrSessionExpired):
		return "WebSocketAuthenticationError", "Authenticated market session expired"
	default:
		return "WebSocketUnavailable", "Unable to establish or maintain the read-only market stream"
	}
}

func preflight(args []string, lookup lookupEnv, output io.Writer, dependencies commandDependencies) error {
	options, err := parseStreamOptions("preflight", args, lookup)
	if err != nil {
		return writePreflightFailure(output, "AUTHENTICATION", "ConfigurationError", "Invalid preflight configuration", 0, 0, false)
	}
	if options.credentialsFile != "" {
		persistedLookup, loadErr := brokerzerodha.LookupWithPersistedSession(brokerzerodha.LookupEnv(lookup), options.credentialsFile)
		err = loadErr
		if err != nil {
			return writePreflightFailure(output, "AUTHENTICATION", "ConfigurationError", "Invalid persisted Zerodha session", 0, 0, false)
		}
		lookup = lookupEnv(persistedLookup)
	}
	if failure, failed := preflightCredentialFailure(lookup); failed {
		if writeErr := writeAuthenticationFailure(output, failure); writeErr != nil {
			return writeErr
		}
		return errors.Join(errAuthentication, errDiagnosticReported)
	}
	loader := dependencies.loadTokens
	var tokens []string
	if loader == nil {
		tokens, err = loadPreflightTokens(options.bundlePath, options.mode)
	} else {
		tokens, err = loader(options.bundlePath)
	}
	if err != nil {
		return writePreflightFailure(output, "AUTHENTICATION", "ConfigurationError", "Invalid checksum-pinned runtime bundle", 0, 0, false)
	}
	applicationCommit, bundleChecksum := "", ""
	if options.outputPath != "" {
		commitLoader := dependencies.currentCommit
		if commitLoader == nil {
			commitLoader = repositoryCommit
		}
		checksumLoader := dependencies.loadBundleChecksum
		if checksumLoader == nil {
			checksumLoader = runtimeBundleChecksum
		}
		applicationCommit, err = commitLoader()
		if err == nil {
			bundleChecksum, err = checksumLoader(options.bundlePath)
		}
		if err != nil {
			return writePreflightFailure(output, "AUTHENTICATION", "ConfigurationError", "Unable to bind preflight evidence", 0, 0, false)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()
	session, err := authenticatedSession(ctx, lookup, dependencies)
	if err != nil {
		var failure brokerzerodha.AuthenticationFailure
		if errors.As(err, &failure) {
			if writeErr := writeAuthenticationFailure(output, failure); writeErr != nil {
				return writeErr
			}
			return errors.Join(errAuthentication, errDiagnosticReported)
		}
		return writePreflightFailure(output, "AUTHENTICATION", "AuthenticationError", "Zerodha authentication failed", 0, 0, false)
	}
	snapshot := session.Snapshot()
	if snapshot.State != brokerzerodha.SessionAuthenticated || snapshot.ExpiresAt.IsZero() {
		session.Shutdown()
		return writePreflightFailure(output, "AUTHENTICATION", "AuthenticationError", "Zerodha authentication failed", 0, 0, true)
	}
	if options.credentialsFile != "" {
		if err = session.PersistAccessToken(options.credentialsFile); err != nil {
			session.Shutdown()
			return writePreflightFailure(output, "AUTHENTICATION", "CredentialPersistenceError", "Unable to persist restored Zerodha session", 0, 0, true)
		}
	}

	client, err := readOnlyClient(lookup, session, dependencies)
	if err != nil {
		session.Shutdown()
		return writePreflightFailure(output, "REST_AUTH", "RESTVerificationError", "Read-only REST verification failed", 0, 0, true)
	}
	if _, err = client.Capabilities(ctx); err != nil {
		client.Shutdown()
		return writePreflightFailure(output, "REST_AUTH", "RESTVerificationError", "Read-only REST verification failed", 0, 0, true)
	}

	streamResult := verifyWebSocketSession(ctx, tokens, options.maxAge, session, dependencies)
	client.Shutdown()
	shutdown := streamResult.shutdown && session.Snapshot().State == brokerzerodha.SessionStopped
	if !streamResult.passed {
		failure := writePreflightFailure(output, "WEBSOCKET_AUTH", streamResult.errorType, streamResult.message, 0, streamResult.observations, shutdown)
		writeWebSocketDiagnostics(output, streamResult)
		return failure
	}
	if _, err = fmt.Fprintf(output, "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=PASS\nOBSERVATIONS_RECEIVED=%d\nSHUTDOWN=PASS\n", streamResult.observations); err != nil {
		return err
	}
	writeWebSocketDiagnostics(output, streamResult)
	if _, err = fmt.Fprintf(output, "ACCESS_TOKEN_EXPIRES_AT=%s\n", snapshot.ExpiresAt.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if options.outputPath != "" {
		evidence := marketvalidation.ZerodhaPreflightEvidence{
			SchemaVersion: marketvalidation.ZerodhaPreflightEvidenceSchemaVersion, ApplicationCommit: applicationCommit, TradingDate: options.tradingDate, Mode: options.mode, RuntimeBundleChecksum: bundleChecksum,
			Timestamp: operatorClock(dependencies).Now().UTC(), AuthenticationPass: true, RESTAuthPass: true, WebSocketAuthPass: true, ExpectedTokenCount: streamResult.expectedTokenCount, ExpectedTokensValid: streamResult.expectedTokensValid,
			ObservationsReceived: streamResult.observations, FreshObservations: streamResult.freshObservations, ShutdownPass: shutdown, TextMessagesReceived: streamResult.textMessages, BrokerMessagesReceived: streamResult.brokerMessages,
			InstrumentsMetaReceived: streamResult.instrumentMetadata, AppCodeReceived: streamResult.appCodeMessages, OrderUpdatesReceived: streamResult.orderUpdates, ProviderErrorsReceived: streamResult.providerErrors,
			BinaryFramesReceived: streamResult.binaryFrames, HeartbeatsReceived: streamResult.heartbeats, PacketsReceived: streamResult.packets, IndexPacketsReceived: streamResult.indexPackets, PacketsDecoded: streamResult.packetsDecoded,
			PacketsRejected: streamResult.packetsRejected, TokenMatches: streamResult.tokenMatches, LastFailureStage: streamResult.lastFailureStage, AccessTokenExpiresAt: snapshot.ExpiresAt.UTC(),
		}
		raw, encodeErr := marketvalidation.EncodeZerodhaPreflightEvidence(evidence)
		if encodeErr != nil {
			return errInvalidConfiguration
		}
		publisher := dependencies.publishEvidence
		if publisher == nil {
			publisher = marketvalidation.PublishEvidenceCreateOnce
		}
		checksum, publishErr := publisher(options.outputPath, raw)
		if publishErr != nil {
			_, _ = fmt.Fprintln(output, "EVIDENCE=FAIL")
			return errInvalidConfiguration
		}
		_, err = fmt.Fprintf(output, "EVIDENCE=PASS\nEVIDENCE_PATH=%s\nEVIDENCE_SHA256=%s\n", options.outputPath, checksum)
	}
	return err
}

func repositoryCommit() (string, error) {
	raw, err := exec.Command("git", "rev-parse", "HEAD").Output()
	value := strings.TrimSpace(string(raw))
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(value) {
		return "", errInvalidConfiguration
	}
	return value, nil
}

func runtimeBundleChecksum(path string) (string, error) {
	bundle, err := config.LoadRuntimeBundle(path)
	return bundle.Checksum, err
}

func readOnlyClient(lookup lookupEnv, session *brokerzerodha.SessionManager, dependencies commandDependencies) (*brokerzerodha.Client, error) {
	zerodhaConfig, err := loadReadOnlyConfig(lookup)
	if err != nil {
		return nil, err
	}
	transport, err := brokerzerodha.NewHTTPTransport(zerodhaConfig, dependencies.roundTripper)
	if err != nil {
		return nil, err
	}
	client, err := brokerzerodha.NewClient(transport, session, operatorClock(dependencies), nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	return client, nil
}

func writePreflightFailure(output io.Writer, stage, errorType, message string, status, observations int, shutdown bool) error {
	if stage == "AUTHENTICATION" {
		_, _ = fmt.Fprintf(output, "AUTHENTICATION=FAIL\nERROR_TYPE=%s\nMESSAGE=%s\nHTTP_STATUS=%d\n", errorType, message, status)
	} else if stage == "REST_AUTH" {
		_, _ = fmt.Fprintf(output, "AUTHENTICATION=PASS\nREST_AUTH=FAIL\nERROR_TYPE=%s\nMESSAGE=%s\nHTTP_STATUS=%d\nSHUTDOWN=%s\n", errorType, message, status, passFail(shutdown))
	} else {
		_, _ = fmt.Fprintf(output, "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=FAIL\nOBSERVATIONS_RECEIVED=%d\nSHUTDOWN=%s\nERROR_TYPE=%s\nMESSAGE=%s\nHTTP_STATUS=%d\n", observations, passFail(shutdown), errorType, message, status)
	}
	return errors.Join(errDiagnosticReported, errWebSocketVerification)
}

func passFail(value bool) string {
	if value {
		return "PASS"
	}
	return "FAIL"
}

func writeWebSocketDiagnostics(output io.Writer, result webSocketResult) {
	_, _ = fmt.Fprintf(output, "WEBSOCKET_HANDSHAKE=%s\nSUBSCRIBE_SENT=%s\nEXPECTED_TOKEN_COUNT=%d\nEXPECTED_TOKENS_VALID=%s\nTEXT_MESSAGES_RECEIVED=%d\nBROKER_MESSAGES_RECEIVED=%d\nINSTRUMENTS_META_RECEIVED=%d\nAPP_CODE_RECEIVED=%d\nORDER_UPDATES_RECEIVED=%d\nPROVIDER_ERRORS_RECEIVED=%d\nBINARY_FRAMES_RECEIVED=%d\nHEARTBEATS_RECEIVED=%d\nPACKETS_RECEIVED=%d\nINDEX_PACKETS_RECEIVED=%d\nPACKETS_DECODED=%d\nPACKETS_REJECTED=%d\nTOKEN_MATCHES=%d\nFRESH_OBSERVATIONS=%d\nLAST_FAILURE_STAGE=%s\n",
		passFail(result.handshake), passFail(result.subscribeSent), result.expectedTokenCount, passFail(result.expectedTokensValid), result.textMessages, result.brokerMessages, result.instrumentMetadata, result.appCodeMessages, result.orderUpdates, result.providerErrors, result.binaryFrames, result.heartbeats, result.packets, result.indexPackets, result.packetsDecoded, result.packetsRejected, result.tokenMatches, result.freshObservations, result.lastFailureStage)
	for _, frame := range result.frameDiagnostics {
		_, _ = fmt.Fprintf(output, "FRAME_SEQUENCE=%d\nFRAME_MESSAGE_TYPE=%s\nFRAME_LENGTH=%d\nFRAME_CLASSIFICATION=%s\n", frame.Sequence, frame.MessageType, frame.Length, frame.Classification)
		if frame.CloseCode != 0 {
			_, _ = fmt.Fprintf(output, "CLOSE_CODE=%d\n", frame.CloseCode)
		}
		if frame.TextMessageType != "" {
			_, _ = fmt.Fprintf(output, "TEXT_MESSAGE_TYPE=%s\n", frame.TextMessageType)
		}
	}
}

func authenticatedSession(ctx context.Context, lookup lookupEnv, dependencies commandDependencies) (*brokerzerodha.SessionManager, error) {
	zerodhaConfig, err := loadReadOnlyConfig(lookup)
	if err != nil {
		return nil, err
	}
	credentials, err := (brokerzerodha.EnvCredentialSource{Lookup: brokerzerodha.LookupEnv(lookup)}).Load(ctx)
	if err != nil {
		return nil, err
	}
	exchanger, err := brokerzerodha.NewHTTPTokenExchanger(zerodhaConfig, dependencies.roundTripper, operatorClock(dependencies))
	if err != nil {
		return nil, err
	}
	session := brokerzerodha.NewSessionManager(credentials, exchanger, operatorClock(dependencies), nil)
	if err = session.Authenticate(ctx); err != nil {
		session.Shutdown()
		return nil, err
	}
	return session, nil
}

func writeAuthenticationFailure(output io.Writer, failure brokerzerodha.AuthenticationFailure) error {
	_, err := fmt.Fprintf(output, "AUTHENTICATION=FAIL\nERROR_TYPE=%s\nMESSAGE=%s\nHTTP_STATUS=%d\n", failure.ErrorType, failure.Message, failure.HTTPStatus)
	return err
}

func exchangePreflightFailure(lookup lookupEnv) (brokerzerodha.AuthenticationFailure, bool) {
	readOnly, present := lookup(readOnlyEnvironment)
	if !present || !strings.EqualFold(strings.TrimSpace(readOnly), "true") {
		return configurationFailure(readOnlyEnvironment + " must be true"), true
	}
	for _, name := range []string{apiKeyEnvironment, apiSecretEnvironment, requestTokenEnvironment} {
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" {
			return configurationFailure("Missing required environment variable: " + name), true
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return configurationFailure("Invalid environment variable: " + name), true
		}
	}
	return brokerzerodha.AuthenticationFailure{}, false
}

func preflightCredentialFailure(lookup lookupEnv) (brokerzerodha.AuthenticationFailure, bool) {
	readOnly, present := lookup(readOnlyEnvironment)
	if !present || !strings.EqualFold(strings.TrimSpace(readOnly), "true") {
		return configurationFailure(readOnlyEnvironment + " must be true"), true
	}
	for _, name := range []string{apiKeyEnvironment, apiSecretEnvironment} {
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" {
			return configurationFailure("Missing required environment variable: " + name), true
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return configurationFailure("Invalid environment variable: " + name), true
		}
	}
	request, _ := lookup(requestTokenEnvironment)
	access, _ := lookup(accessTokenEnvironment)
	expiry, _ := lookup(accessExpiryEnvironment)
	if strings.TrimSpace(access) == "" && strings.TrimSpace(request) == "" {
		return configurationFailure("A restored access token or fresh request token is required"), true
	}
	if (strings.TrimSpace(access) == "") != (strings.TrimSpace(expiry) == "") {
		return configurationFailure("Restored access token and expiry must be provided together"), true
	}
	return brokerzerodha.AuthenticationFailure{}, false
}

func configurationFailure(message string) brokerzerodha.AuthenticationFailure {
	return brokerzerodha.AuthenticationFailure{ErrorType: "ConfigurationError", Message: message, HTTPStatus: 0}
}

func loadReadOnlyConfig(lookup lookupEnv) (brokerzerodha.Config, error) {
	value, err := brokerzerodha.LoadConfig(brokerzerodha.LookupEnv(lookup))
	if err != nil || !value.Enabled {
		return brokerzerodha.Config{}, errInvalidConfiguration
	}
	return value, nil
}

func loadPreflightTokens(path, mode string) ([]string, error) {
	if path == "" {
		return nil, errInvalidConfiguration
	}
	bundle, err := config.LoadRuntimeBundle(path)
	if err != nil || bundle.Manifest.Mode != mode {
		return nil, errInvalidConfiguration
	}
	if mode == "PAPER" {
		return loadDay0BundleTokens(bundle)
	}
	if mode == "SHADOW" {
		return loadShadowBundleTokens(bundle)
	}
	return nil, errInvalidConfiguration
}

func loadDay0BundleTokens(bundle config.RuntimeBundle) ([]string, error) {
	if bundle.Watchlist.ID != day0WatchlistID || len(bundle.Watchlist.Requirements) != 2 || len(bundle.Tokens) != 2 {
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

func loadShadowBundleTokens(bundle config.RuntimeBundle) ([]string, error) {
	if bundle.Watchlist.ID != shadowWatchlistID || len(bundle.Watchlist.Requirements) != 14 || len(bundle.Tokens) != 14 {
		return nil, errInvalidConfiguration
	}
	counts := map[domain.InstrumentType]int{}
	underlyings := map[domain.UnderlyingID]int{}
	for _, requirement := range bundle.Watchlist.Requirements {
		instrument, found := bundle.Master.Instrument(requirement.InstrumentID)
		if !found || requirement.Provider != domain.Provider("zerodha") || requirement.EventKind != marketmodel.EventKindQuote || !requirement.Required || requirement.Exchange != domain.ExchangeNSE {
			return nil, errInvalidConfiguration
		}
		underlying := instrument.UnderlyingID()
		if underlying != "NIFTY" && underlying != "BANKNIFTY" {
			return nil, errInvalidConfiguration
		}
		counts[instrument.Type()]++
		underlyings[underlying]++
	}
	if counts[domain.InstrumentIndex] != 2 || counts[domain.InstrumentFuture] != 2 || counts[domain.InstrumentOption] != 10 || underlyings["NIFTY"] != 7 || underlyings["BANKNIFTY"] != 7 {
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
