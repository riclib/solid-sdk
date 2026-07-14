//go:build linux && arm64

package quack

import "embed"

// embedded contains the gzip-compressed .duckdb_extension binaries for the
// build arch, committed under linux_arm64/ as *.duckdb_extension.gz
// (SHA-pinned via extensions.lock, refreshed by scripts/duckdb-fetch.sh on a
// pin bump) and gunzipped at materialize time.
//
//go:embed linux_arm64/*.duckdb_extension.gz
var embedded embed.FS

const archDir = "linux_arm64"
