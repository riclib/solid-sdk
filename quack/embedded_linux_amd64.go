//go:build linux && amd64

package quack

import "embed"

// embedded contains the gzip-compressed .duckdb_extension binaries for the
// build arch, staged under linux_amd64/ as *.duckdb_extension.gz by
// scripts/duckdb-fetch.sh (not committed; pinned via extensions.lock) and
// gunzipped at materialize time.
//
//go:embed linux_amd64/*.duckdb_extension.gz
var embedded embed.FS

const archDir = "linux_amd64"
