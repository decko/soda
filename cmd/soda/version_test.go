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

func TestBinaryVersionID(t *testing.T) {
	id := binaryVersionID()

	if id == "" {
		t.Error("binaryVersionID() should not be empty")
	}

	// Must always contain the version variable.
	if !strings.HasPrefix(id, version) {
		t.Errorf("binaryVersionID() = %q, should start with version %q", id, version)
	}

	// Calling twice should return the same value (deterministic).
	id2 := binaryVersionID()
	if id != id2 {
		t.Errorf("binaryVersionID() not deterministic: %q != %q", id, id2)
	}
}
