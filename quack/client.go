package quack

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/duckdb/duckdb-go/v2"
)

// client is the low-level quack transport: a LOCAL DuckDB handle with the
// quack extension loaded, dispatching every statement server-side via
// quack_query. The local handle holds no engine data — it is transport only.
// Adapted from the platform's infra/quack.Client; the wire it speaks is pinned
// by extensions.lock in lockstep with the platform's serving engines.
type client struct {
	uri   string
	token string
	tls   bool
	db    *sql.DB
}

// dial opens the local transport handle against the engine at uri. extDir is
// the materialized extension-install dir (installDir); the extension load is
// air-gapped — there is deliberately NO network INSTALL fallback.
func dial(uri, token string, tls bool, extDir string) (*client, error) {
	if uri == "" {
		return nil, fmt.Errorf("quack: uri required")
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("quack: open client duckdb: %w", err)
	}
	// One warmed connection so the LOAD below covers every statement we run
	// (the extension load is per-connection; a pool would need re-loading).
	db.SetMaxOpenConns(1)
	stmts := []string{
		fmt.Sprintf("SET extension_directory = '%s'", escapeSingleQuotes(extDir)),
		"SET autoinstall_known_extensions = false",
		"LOAD quack",
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("quack: extension on client (%q): %w", stmt, err)
		}
	}
	return &client{uri: uri, token: token, tls: tls, db: db}, nil
}

// exec runs a write statement server-side via quack_query; the result is
// discarded.
func (c *client) exec(ctx context.Context, stmt string) error {
	if _, err := c.db.ExecContext(ctx, c.wrap(stmt)); err != nil {
		return fmt.Errorf("quack exec: %w", err)
	}
	return nil
}

// query runs a read statement server-side via quack_query and streams the
// rows. The caller owns closing the returned *sql.Rows.
func (c *client) query(ctx context.Context, stmt string) (*sql.Rows, error) {
	rows, err := c.db.QueryContext(ctx, c.wrap(stmt))
	if err != nil {
		return nil, fmt.Errorf("quack query: %w", err)
	}
	return rows, nil
}

// close releases the local transport handle. It does NOT stop the remote
// engine.
func (c *client) close() error {
	if c.db != nil {
		err := c.db.Close()
		c.db = nil
		return err
	}
	return nil
}

// wrap builds the quack_query call that ships stmt to the engine. disable_ssl
// follows the handshake's tls field: loopback engines serve plaintext today;
// when the platform flips tls true the client speaks SSL with no wire change.
func (c *client) wrap(stmt string) string {
	return fmt.Sprintf(
		"SELECT * FROM quack_query('%s', '%s', token=>'%s', disable_ssl=>%t)",
		escapeSingleQuotes(c.uri), escapeSingleQuotes(stmt), escapeSingleQuotes(c.token), !c.tls,
	)
}

// escapeSingleQuotes doubles single quotes so a value is safe inside a
// single-quoted SQL string literal.
func escapeSingleQuotes(s string) string {
	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	return string(out)
}
