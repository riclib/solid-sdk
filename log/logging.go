// Package log is the shared structured-logging package for the Solid platform
// (v4) and partner solutions. It wraps slog with multiple destinations (app
// logger -> stdout + file, NATS logger -> file only, HTTP logger -> file only),
// shared field-key conventions, and an optional NATS slog.Handler that ships
// structured records to the platform over the bus.
//
// It is CGO-free and stdlib-plus-two-pretty-printers only. Importing this
// package for plain Pkg() logging does NOT pull in a live NATS connection —
// the NATS handler is constructed explicitly (see nats_handler.go), never in
// an init().
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/dusted-go/logging/prettylog"
	"github.com/phsym/console-slog"
)

// Loggers holds all application loggers.
type Loggers struct {
	App  *slog.Logger // Main app logger (stdout + file)
	NATS *slog.Logger // NATS server logger (file only)
	HTTP *slog.Logger // HTTP access logger (file only)
}

// New creates all loggers based on configuration.
func New(cfg Config) (*Loggers, error) {
	// Ensure log directory exists
	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory %s: %w", cfg.Dir, err)
	}

	// Create app logger (tee to stdout + file)
	appLogger, err := createAppLogger(cfg.Dir, cfg.App)
	if err != nil {
		return nil, fmt.Errorf("failed to create app logger: %w", err)
	}

	// Create NATS logger (file only)
	natsLogger, err := createFileLogger(cfg.Dir, "nats", cfg.NATS)
	if err != nil {
		return nil, fmt.Errorf("failed to create nats logger: %w", err)
	}

	// Create HTTP logger (file only)
	httpLogger, err := createFileLogger(cfg.Dir, "http", cfg.HTTP)
	if err != nil {
		return nil, fmt.Errorf("failed to create http logger: %w", err)
	}

	// Set the base logger so Pkg() derives from the configured handler.
	base = appLogger

	return &Loggers{
		App:  appLogger,
		NATS: natsLogger,
		HTTP: httpLogger,
	}, nil
}

// createAppLogger creates the app logger that writes to both stdout and file
// Stdout gets the configured format (console for colors), file gets plain text
func createAppLogger(dir string, cfg LoggerConfig) (*slog.Logger, error) {
	level := parseLevel(cfg.Level)

	// Open log file
	logPath := filepath.Join(dir, "app.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", logPath, err)
	}

	// Create a multi-handler that writes to both stdout (with colors) and file (plain text)
	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
	}

	// File handler - always plain text
	fileHandler := slog.NewTextHandler(file, opts)

	// Stdout handler - use configured format
	var stdoutHandler slog.Handler
	switch cfg.Format {
	case "console":
		stdoutHandler = console.NewHandler(os.Stdout, &console.HandlerOptions{
			Level:     level,
			AddSource: true,
		})
	case "json":
		stdoutHandler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		stdoutHandler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(&multiHandler{handlers: []slog.Handler{stdoutHandler, fileHandler}}), nil
}

// multiHandler fans out log records to multiple handlers
type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, r.Level) {
			if err := handler.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

// createFileLogger creates a logger that writes only to a file
func createFileLogger(dir, name string, cfg LoggerConfig) (*slog.Logger, error) {
	level := parseLevel(cfg.Level)

	logPath := filepath.Join(dir, name+".log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", logPath, err)
	}

	return createLogger(file, level, cfg.Format), nil
}

// createLogger creates a slog.Logger with the specified writer, level, and format
func createLogger(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
	}

	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts))
	case "text":
		return slog.New(slog.NewTextHandler(w, opts))
	case "console":
		// For console format, use prettylog for colored output
		// But prettylog doesn't take a writer, so fall back to console-slog
		return slog.New(console.NewHandler(w, &console.HandlerOptions{
			Level:     level,
			AddSource: true,
		}))
	case "pretty":
		// prettylog only works with stderr, use it directly
		return slog.New(prettylog.NewHandler(&slog.HandlerOptions{
			Level:     level,
			AddSource: true,
		}))
	default:
		return slog.New(slog.NewTextHandler(w, opts))
	}
}

// parseLevel converts a string log level to slog.Level
func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
