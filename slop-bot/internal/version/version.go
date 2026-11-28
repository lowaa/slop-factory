// Package version reports the build's version, stamped at link time.
package version

// value is overwritten with -ldflags "-X .../internal/version.value=<v>".
var value = "dev"

// String returns the version this binary was built as.
func String() string { return value }
