package main

import (
	"strings"
	"testing"
)

func TestBuildVersionString(t *testing.T) {
	out := buildVersionString()

	if !strings.Contains(out, version) {
		t.Errorf("output %q does not contain version %q", out, version)
	}

	if !strings.Contains(out, "go") {
		t.Errorf("output %q does not contain Go version info", out)
	}

	if !strings.HasPrefix(out, "soda version ") {
		t.Errorf("output %q does not start with expected prefix", out)
	}

	if !strings.Contains(out, "commit ") {
		t.Errorf("output %q does not contain commit info", out)
	}
}
