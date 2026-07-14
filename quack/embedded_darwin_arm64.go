//go:build darwin && arm64

package quack

import "embed"

// embedded contains the gzip-compressed .duckdb_extension binaries for the
// build arch, committed under osx_arm64/ as *.duckdb_extension.gz
// (SHA-pinned via extensions.lock, refreshed by scripts/duckdb-fetch.sh on a
// pin bump) and gunzipped at materialize time.
//
//go:embed osx_arm64/*.duckdb_extension.gz
var embedded embed.FS

const archDir = "osx_arm64"
