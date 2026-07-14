package quack

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
	"github.com/riclib/solid-sdk/transport"
)

// DefaultExtensionsDir is where the embedded quack/httpfs extensions are
// materialized when WithExtensionsDir is not given — the same convention as
// the platform's data/duckdb-extensions.
const DefaultExtensionsDir = "data/duckdb-extensions"

// Conn is a governed quack connection to a bound workspace's engine — the
// paved road for the S-1721/S-1728 contract. It owns three obligations so the
// solution author doesn't have to:
//
//   - THE HANDSHAKE: the engine address and per-boot token are never
//     configured; Connect resolves them from the platform over the store-proxy
//     wire (transport.QuackConnect — the binding is the grant).
//   - THE RECONNECT CONTRACT: tokens are per engine boot and engines are
//     disposable (LRU evict / compaction / restart / crash). On a failed
//     statement, Conn re-runs the handshake as a boot-identity probe: if the
//     platform hands back a NEW {uri, token} the old engine was retired — Conn
//     reconnects and retries the statement ONCE; if the handle is unchanged
//     the engine is the same boot and the failure was the statement's own,
//     returned as-is. No error-string matching, no persisted handles. The
//     retried statement had failed on a retired engine, but make your writes
//     idempotent anyway (upsert shapes) — in-flight statements on a dying
//     engine can land before it goes.
//   - ATTRIBUTION: every statement is prefixed with
//     contract.StatementMarker(solution) so the engine's statement log carries
//     statement-grain attribution (the peek doctrine).
//
// A Conn is safe for concurrent use. It holds {uri, token} strictly in memory,
// never logs the token, and never persists a handle.
type Conn struct {
	nc        *nats.Conn
	solution  string
	workspace string
	marker    string
	extDir    string // materialized install dir (SET extension_directory)

	mu     sync.Mutex
	client *client
	uri    string
	token  string
}

// Option configures Connect.
type Option func(*options)

type options struct {
	extensionsDir string
}

// WithExtensionsDir sets the directory the embedded quack/httpfs extensions
// are materialized under (default DefaultExtensionsDir). The directory is
// created if missing; staging is idempotent and shared across connections.
func WithExtensionsDir(dir string) Option {
	return func(o *options) { o.extensionsDir = dir }
}

// Connect runs the workspace-engine handshake and opens the governed
// connection. solution is the calling solution's announced name; workspace is
// the bound workspace's slug. The ctx bounds the handshake (the platform side
// may include an engine boot — give it a few seconds).
//
// Liveness follows the store-proxy split: a transport failure (platform down,
// NATS timeout) and a policy denial (not_granted — unknown workspace or the
// workspace doesn't draw this solution) both surface as Go errors here,
// because a Conn cannot exist without a granted engine; the denial error
// carries the platform's code for callers that need to tell them apart.
func Connect(ctx context.Context, nc *nats.Conn, solution, workspace string, opts ...Option) (*Conn, error) {
	o := options{extensionsDir: DefaultExtensionsDir}
	for _, opt := range opts {
		opt(&o)
	}
	extDir, err := installDir(o.extensionsDir)
	if err != nil {
		return nil, err
	}
	c := &Conn{
		nc:        nc,
		solution:  solution,
		workspace: workspace,
		marker:    contract.StatementMarker(solution),
		extDir:    extDir,
	}
	res, err := c.handshake(ctx)
	if err != nil {
		return nil, err
	}
	cl, err := dial(res.URI, res.Token, res.TLS, extDir)
	if err != nil {
		return nil, err
	}
	c.client, c.uri, c.token = cl, res.URI, res.Token
	return c, nil
}

// Exec runs a write statement server-side on the workspace engine, marker-
// prefixed, with the reconnect contract applied (one re-handshake + retry when
// the engine was retired).
func (c *Conn) Exec(ctx context.Context, stmt string) error {
	_, err := c.run(ctx, stmt, func(cl *client, s string) (*sql.Rows, error) {
		return nil, cl.exec(ctx, s)
	})
	return err
}

// Query runs a read statement server-side and streams the rows, marker-
// prefixed, with the reconnect contract applied at issue time (a stream that
// dies mid-scan surfaces on the returned rows and is the caller's retry). The
// caller owns closing the returned *sql.Rows.
func (c *Conn) Query(ctx context.Context, stmt string) (*sql.Rows, error) {
	return c.run(ctx, stmt, func(cl *client, s string) (*sql.Rows, error) {
		return cl.query(ctx, s)
	})
}

// Close releases the local transport handle. It does NOT stop the remote
// engine (engines are platform-owned).
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	err := c.client.close()
	c.client = nil
	return err
}

// run executes op against the current client and applies the reconnect
// contract on failure: re-handshake as a boot-identity probe, reconnect +
// retry once only when the platform hands back a different {uri, token}.
func (c *Conn) run(ctx context.Context, stmt string, op func(*client, string) (*sql.Rows, error)) (*sql.Rows, error) {
	marked := c.marker + stmt

	c.mu.Lock()
	cl, uri, token := c.client, c.uri, c.token
	c.mu.Unlock()
	if cl == nil {
		return nil, fmt.Errorf("quack: connection is closed")
	}

	rows, err := op(cl, marked)
	if err == nil {
		return rows, nil
	}

	changed, rerr := c.refresh(ctx, uri, token)
	if rerr != nil {
		// The statement failed AND the re-handshake failed — the engine (or the
		// platform) is genuinely unavailable. Surface both.
		return nil, fmt.Errorf("%w (re-handshake: %v)", err, rerr)
	}
	if !changed {
		// Same engine boot — the failure was the statement's own. No retry.
		return nil, err
	}

	c.mu.Lock()
	cl = c.client
	c.mu.Unlock()
	if cl == nil {
		return nil, fmt.Errorf("quack: connection is closed")
	}
	return op(cl, marked)
}

// refresh re-runs the handshake and swaps the client if the engine handle
// changed since (usedURI, usedToken). Returns changed=true when the caller
// should retry — either this call swapped the client, or a concurrent caller
// already had.
func (c *Conn) refresh(ctx context.Context, usedURI, usedToken string) (changed bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return false, fmt.Errorf("quack: connection is closed")
	}
	// A concurrent failure already refreshed past the handle we used.
	if c.uri != usedURI || c.token != usedToken {
		return true, nil
	}
	res, err := c.handshake(ctx)
	if err != nil {
		return false, err
	}
	if res.URI == c.uri && res.Token == c.token {
		return false, nil
	}
	cl, err := dial(res.URI, res.Token, res.TLS, c.extDir)
	if err != nil {
		return false, err
	}
	_ = c.client.close()
	c.client, c.uri, c.token = cl, res.URI, res.Token
	return true, nil
}

// handshake wraps transport.QuackConnect, folding a policy denial into a Go
// error (a Conn cannot exist ungranted) that carries the platform's code. It
// never includes the token in any error.
func (c *Conn) handshake(ctx context.Context) (contract.QuackConnectResult, error) {
	res, err := transport.QuackConnect(ctx, c.nc, c.solution, c.workspace)
	if err != nil {
		return res, err
	}
	if res.Code != "" {
		return res, fmt.Errorf("quack connect %s/%s: %s (code %s)", c.solution, c.workspace, res.Error, res.Code)
	}
	if res.URI == "" || res.Token == "" {
		return res, fmt.Errorf("quack connect %s/%s: platform reply missing uri/token", c.solution, c.workspace)
	}
	return res, nil
}
