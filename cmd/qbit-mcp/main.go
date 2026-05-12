// Command qbit-mcp is the qBittorrent MCP server entrypoint.
//
// It exposes MCP tools wrapping the qBittorrent WebUI v2 API over either the
// stdio transport (for local agents) or the streamable HTTP transport (for
// k8s deployments). The server is intended to run as a sidecar to the
// qBittorrent container, reaching it over loopback with WebUI auth bypassed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	qbt "github.com/autobrr/go-qbittorrent"

	mcppkg "github.com/wyvernzora/qbittorrent-mcp/internal/mcp"
)

// version is overridable at link time via -ldflags="-X main.version=...".
var version = "0.1.0"

const (
	defaultQbURL   = "http://localhost:8080"
	defaultTimeout = 15 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "qbit-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	envWarnings := []envWarning{}
	var (
		showVersion = flag.Bool("version", false, "print version and exit")
		transport   = stringFlag("transport", "QBITTORRENT_TRANSPORT", "stdio", "transport: stdio or http")
		addr        = stringFlag("addr", "QBITTORRENT_ADDR", ":8080", "listen address (http transport only)")
		qbURL       = stringFlag("qb-url", "QBITTORRENT_URL", defaultQbURL, "qBittorrent WebUI base URL (sidecar → loopback)")
		qbTimeout   = durationFlag("qb-timeout", "QBITTORRENT_TIMEOUT", defaultTimeout, "per-request HTTP timeout against qBittorrent", &envWarnings)
		logLevel    = stringFlag("log-level", "QBITTORRENT_LOG_LEVEL", "info", "log level: debug, info, warn, error")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	for _, w := range envWarnings {
		logger.Warn("invalid env value, falling back to default",
			"env", w.Env,
			"value", w.Value,
			"error", w.Err,
		)
	}

	// autobrr/go-qbittorrent.Config.Timeout is seconds-as-int. Round up so
	// sub-second values still get at least one second of headroom.
	timeoutSec := int((*qbTimeout + time.Second - 1) / time.Second)
	client := qbt.NewClient(qbt.Config{
		Host:    *qbURL,
		Timeout: timeoutSec,
		// Username/Password intentionally empty: the sidecar deployment
		// relies on qBittorrent's loopback-auth-bypass. LoginCtx is a no-op
		// when both creds are empty, so authenticated endpoints work
		// directly without a login round-trip.
		Log: log.New(slogWriter{logger: logger.With("component", "qbt")}, "", 0),
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server := mcppkg.New(client, logger, version)

	switch *transport {
	case "stdio":
		logger.Info("starting qbit-mcp", "transport", "stdio", "version", version, "qb_url", *qbURL)
		return mcppkg.RunStdio(ctx, server)
	case "http":
		logger.Info("starting qbit-mcp", "transport", "http", "addr", *addr, "version", version, "qb_url", *qbURL)
		return mcppkg.RunHTTP(ctx, server, *addr, logger)
	default:
		return fmt.Errorf("invalid --transport %q (want stdio or http)", *transport)
	}
}

// slogWriter adapts a *slog.Logger to io.Writer so the standard *log.Logger
// the autobrr client expects emits its lines as structured slog records.
type slogWriter struct{ logger *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	msg := string(p)
	// log.Logger always appends a newline; strip it so it doesn't show up
	// inside the slog message.
	if n := len(msg); n > 0 && msg[n-1] == '\n' {
		msg = msg[:n-1]
	}
	w.logger.Debug(msg)
	return len(p), nil
}

type envWarning struct {
	Env, Value string
	Err        error
}

func stringFlag(name, env, def, usage string) *string {
	if v := os.Getenv(env); v != "" {
		def = v
	}
	return flag.String(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

func durationFlag(name, env string, def time.Duration, usage string, warnings *[]envWarning) *time.Duration {
	if v := os.Getenv(env); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			*warnings = append(*warnings, envWarning{Env: env, Value: v, Err: err})
		} else {
			def = d
		}
	}
	return flag.Duration(name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

func parseLogLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid --log-level %q", s)
	}
}
