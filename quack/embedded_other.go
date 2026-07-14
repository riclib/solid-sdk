//go:build !(linux && amd64) && !(darwin && arm64) && !(linux && arm64)

package quack

import "embed"

// embedded is empty on archs not declared in extensions.lock. Connect fails
// loudly with a clear "add this arch to extensions.lock" message. Code paths
// that don't open a quack connection are unaffected.
var embedded embed.FS

const archDir = ""
