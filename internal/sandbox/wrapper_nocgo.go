//go:build !cgo

package sandbox

// WrapperBinaryPath returns an empty string when cgo is disabled.
// Sandbox support (including the arapuca wrapper binary) requires cgo.
func WrapperBinaryPath() string {
	return ""
}
