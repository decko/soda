//go:build cgo

package sandbox

import arapuca "github.com/sergio-correia/go-arapuca"

// WrapperBinaryPath returns the path to the arapuca wrapper binary,
// or an empty string if the binary is not found. The wrapper binary
// is required for Landlock/seccomp enforcement at runtime.
func WrapperBinaryPath() string {
	return arapuca.WrapperPath()
}
