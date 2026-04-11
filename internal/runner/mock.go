package runner

import (
	"context"
	"fmt"
	"sync"
)

// MockRunner returns canned responses from fixture data for testing.
type MockRunner struct {
	mu        sync.Mutex
	Responses map[string]*RunResult // phase name -> canned response
	Errors    map[string]error      // phase name -> error to return
	Calls     []RunOpts             // recorded calls for assertions
}

// Run records the call and returns the configured response or error.
func (m *MockRunner) Run(ctx context.Context, opts RunOpts) (*RunResult, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, opts)
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err, ok := m.Errors[opts.Phase]; ok {
		return nil, err
	}
	if result, ok := m.Responses[opts.Phase]; ok {
		return result, nil
	}
	return nil, fmt.Errorf("mock: no response configured for phase %q", opts.Phase)
}
