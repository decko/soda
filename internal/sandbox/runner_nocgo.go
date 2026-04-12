//go:build !cgo

package sandbox

import (
	"context"
	"fmt"

	"github.com/decko/soda/internal/runner"
)

// Runner is a stub that fails at construction when cgo is unavailable.
type Runner struct{}

// compile-time interface check
var _ runner.Runner = (*Runner)(nil)

// New returns an error because sandbox support requires cgo (go-arapuca uses cgo).
func New(_ Config) (*Runner, error) {
	return nil, fmt.Errorf("sandbox: cgo is required for sandbox support; rebuild with CGO_ENABLED=1")
}

// Close is a no-op.
func (r *Runner) Close() {}

// Run always fails because sandbox support requires cgo.
func (r *Runner) Run(_ context.Context, _ runner.RunOpts) (*runner.RunResult, error) {
	return nil, fmt.Errorf("sandbox: cgo is required for sandbox support; rebuild with CGO_ENABLED=1")
}
