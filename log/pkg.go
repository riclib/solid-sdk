package log

import "log/slog"

// base is the configured app logger, set by New().
// All package loggers derive from this to inherit the correct handler.
var base *slog.Logger

// Pkg creates a logger with domain/pkg attrs baked in.
// Call once per package during startup and store the result.
//
//	log := log.Pkg("store", "postgres")
//	log.WarnContext(ctx, "connection failed", "id", id, "error", err)
func Pkg(domain, pkg string) *slog.Logger {
	b := base
	if b == nil {
		b = slog.Default()
	}
	return b.With("domain", domain, "pkg", pkg)
}
