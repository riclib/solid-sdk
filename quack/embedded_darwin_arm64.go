//go:build darwin && arm64

package quack

import "embed"

// embedded contains the gzip-compressed .duckdb_extension binaries for the
// build arch, staged under osx_arm64/ as *.duckdb_extension.gz by
// scripts/duckdb-fetch.sh (not committed; pinned via extensions.lock) and
// gunzipped at materialize time.
//
//go:embed osx_arm64/*.duckdb_extension.gz
var embedded embed.FS

const archDir = "osx_arm64"
