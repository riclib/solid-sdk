// Air-gap-safe staging of the DuckDB extensions the quack client needs (quack +
// httpfs), embedded into the binary at compile time.
//
// The extensions are staged per arch by scripts/duckdb-fetch.sh (build-time
// network, SHA-verified against extensions.lock), embedded via //go:embed, and
// materialized to an extension-install layout directory the first time a
// connection opens. NO RUNTIME NETWORK ACCESS — a solution binary works
// offline, in CI, and on sovereign estates whose runtimes can't phone home to
// extensions.duckdb.org. This mirrors the platform's infra/duckdb/extensions
// mechanism; the lockfile pins move in lockstep with the platform's (the
// client and the serving engine must speak the same quack protocol).
package quack

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// extSuffix is the embedded suffix. Extensions are staged and embedded
// gzip-compressed (~3x smaller in the binary); gunzip happens at materialize
// time.
const extSuffix = ".duckdb_extension.gz"

// clientExtensions is what the client transport needs: quack (quack_query) and
// httpfs (its HTTP transport, autoloaded from the install dir).
var clientExtensions = []string{"quack", "httpfs"}

//go:embed extensions.lock
var lockfileBytes []byte

// duckdbVersion reads the engine version from the embedded lockfile in
// DuckDB's directory form, e.g. "v1.5.3".
func duckdbVersion() string {
	for _, line := range strings.Split(string(lockfileBytes), "\n") {
		if rest, ok := strings.CutPrefix(line, "duckdb_version:"); ok {
			return "v" + strings.TrimSpace(rest)
		}
	}
	return ""
}

// installDirCache dedupes materialization per base dir: one process opens many
// connections but stages the extensions once.
var (
	installMu    sync.Mutex
	installCache = map[string]*installEntry{}
)

type installEntry struct {
	once sync.Once
	dir  string
	err  error
}

// installDir materializes the client extensions into a DuckDB extension-install
// layout — <base>/install/v<version>/<arch>/<name>.duckdb_extension — and
// returns <base>/install, suitable for `SET extension_directory = '<dir>'`.
//
// An install DIR (not a single LOAD '<path>') is required: with autoload left
// on, DuckDB resolves httpfs from the directory on whatever connection needs
// it. Idempotent across calls and processes at the same DuckDB version; writes
// are tmp-file + atomic rename.
func installDir(baseDir string) (string, error) {
	installMu.Lock()
	e, ok := installCache[baseDir]
	if !ok {
		e = &installEntry{}
		installCache[baseDir] = e
	}
	installMu.Unlock()
	e.once.Do(func() {
		e.dir, e.err = materializeInstallDir(baseDir)
	})
	return e.dir, e.err
}

func materializeInstallDir(baseDir string) (string, error) {
	if archDir == "" {
		return "", fmt.Errorf("quack: no embedded extensions for this arch — add it to extensions.lock + an embedded_<arch>.go and re-run scripts/duckdb-fetch.sh")
	}
	ver := duckdbVersion()
	if ver == "" {
		return "", fmt.Errorf("quack: could not read duckdb_version from embedded extensions.lock")
	}
	base := filepath.Join(baseDir, "install")
	layoutDir := filepath.Join(base, ver, archDir)
	if err := os.MkdirAll(layoutDir, 0o755); err != nil {
		return "", fmt.Errorf("quack: mkdir %s: %w", layoutDir, err)
	}
	for _, name := range clientExtensions {
		gz, err := embedded.ReadFile(archDir + "/" + name + extSuffix)
		if err != nil {
			return "", fmt.Errorf("quack: %s not embedded for arch %s — run scripts/duckdb-fetch.sh before building: %w", name, archDir, err)
		}
		payload, err := decompressExtension(gz)
		if err != nil {
			return "", fmt.Errorf("quack: %s: %w", name, err)
		}
		target := filepath.Join(layoutDir, name+".duckdb_extension")
		if info, err := os.Stat(target); err == nil && info.Size() == int64(len(payload)) {
			continue // already materialized, identical size
		}
		tmp, err := os.CreateTemp(layoutDir, name+".*.tmp")
		if err != nil {
			return "", fmt.Errorf("quack: create temp: %w", err)
		}
		tmpPath := tmp.Name()
		if _, err := tmp.Write(payload); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return "", fmt.Errorf("quack: write %s: %w", name, err)
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpPath)
			return "", fmt.Errorf("quack: close temp: %w", err)
		}
		if err := os.Rename(tmpPath, target); err != nil {
			os.Remove(tmpPath)
			return "", fmt.Errorf("quack: rename to %s: %w", target, err)
		}
	}
	return base, nil
}

// decompressExtension gunzips embedded bytes into the raw .duckdb_extension
// payload DuckDB's loader consumes.
func decompressExtension(gz []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer r.Close()
	payload, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("embedded extension is empty")
	}
	return payload, nil
}
