#!/usr/bin/env bash
# scripts/duckdb-fetch.sh — refresh the quack-client DuckDB extension binaries
# for ONE arch into the quack/ embed dirs, verified against
# quack/extensions.lock. Idempotent: files already present and SHA-matching
# the lock are left alone.
#
# The .duckdb_extension.gz binaries ARE committed (deliberately unlike solid's
# fetch-at-build packaging: the SDK is consumed as a Go module, and the module
# zip must carry the //go:embed payload or every consumer importing quack
# fails to build). This script is the REFRESH/VERIFY tool for a pin bump: copy
# the new pins from solid's infra/duckdb/extensions/extensions.lock into
# quack/extensions.lock, delete the stale .gz files, run this per arch, and
# commit the result. A plain checkout builds with no network.
#
# Usage: scripts/duckdb-fetch.sh [arch]
#   arch defaults to the host build arch (from `go env GOOS GOARCH`).
#   Pass an explicit lock arch key to stage a cross-compile target, e.g.
#     scripts/duckdb-fetch.sh linux_amd64

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
EXT_DIR="${REPO_ROOT}/quack"
LOCKFILE="${EXT_DIR}/extensions.lock"

if [[ ! -f "${LOCKFILE}" ]]; then
  echo "ERROR: ${LOCKFILE} not found" >&2
  exit 1
fi

# Resolve the target arch → extensions.lock arch key.
ARCH="${1:-}"
if [[ -z "${ARCH}" ]]; then
  GOOS="$(go env GOOS)"
  GOARCH="$(go env GOARCH)"
  case "${GOOS}/${GOARCH}" in
    darwin/arm64) ARCH="osx_arm64" ;;
    linux/amd64)  ARCH="linux_amd64" ;;
    linux/arm64)  ARCH="linux_arm64" ;;
    *)
      echo "ERROR: no DuckDB extension arch mapping for ${GOOS}/${GOARCH}." >&2
      echo "       Supported: darwin/arm64, linux/amd64, linux/arm64." >&2
      echo "       Add the arch to quack/extensions.lock + an embedded_<arch>.go." >&2
      exit 1
      ;;
  esac
fi

VERSION="$(awk -F': *' '/^duckdb_version:/ {print $2; exit}' "${LOCKFILE}")"
if [[ -z "${VERSION}" ]]; then
  echo "ERROR: could not read duckdb_version from ${LOCKFILE}" >&2
  exit 1
fi

# Pull (name, sha256) pairs for the requested arch out of the lock.
PAIRS="$(awk -v want="${ARCH}" '
  /^  - name:/             { name=$3; arch=""; next }
  /^      [A-Za-z0-9_]+:$/ { a=$1; sub(/:$/,"",a); arch=a; next }
  /^        sha256:/       { if (arch==want) print name, $2 }
' "${LOCKFILE}")"

if [[ -z "${PAIRS}" ]]; then
  echo "ERROR: arch '${ARCH}' not present in ${LOCKFILE}." >&2
  exit 1
fi

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

EXT_BASE="https://extensions.duckdb.org/v${VERSION}"
ARCH_DIR="${EXT_DIR}/${ARCH}"
mkdir -p "${ARCH_DIR}"

fetched=0
kept=0
while read -r name want_sha; do
  [[ -z "${name}" ]] && continue
  target="${ARCH_DIR}/${name}.duckdb_extension.gz"
  if [[ -f "${target}" ]] && [[ "$(sha256 "${target}")" == "${want_sha}" ]]; then
    kept=$((kept + 1))
    continue
  fi
  url="${EXT_BASE}/${ARCH}/${name}.duckdb_extension.gz"
  echo "  ${ARCH}/${name}  ←  ${url}"
  tmp="$(mktemp "${ARCH_DIR}/.${name}.XXXXXX")"
  if ! curl -fsSL "${url}" -o "${tmp}"; then
    rm -f "${tmp}"
    echo "ERROR: failed to download ${url}" >&2
    exit 1
  fi
  got_sha="$(sha256 "${tmp}")"
  if [[ "${got_sha}" != "${want_sha}" ]]; then
    rm -f "${tmp}"
    echo "ERROR: SHA mismatch for ${ARCH}/${name}" >&2
    echo "       lock: ${want_sha}" >&2
    echo "       got:  ${got_sha}" >&2
    echo "       (lock out of date? re-copy from solid's extensions.lock)" >&2
    exit 1
  fi
  mv "${tmp}" "${target}"
  fetched=$((fetched + 1))
done <<< "${PAIRS}"

echo "✓ quack-client duckdb extensions ready for ${ARCH} (v${VERSION}): ${fetched} fetched, ${kept} cached"
