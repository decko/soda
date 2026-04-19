package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/config"
)

func TestRunInit_WritesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	var buf bytes.Buffer
	if err := runInit(&buf, dest, false); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	// File must exist.
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("config file is empty")
	}

	// File must be valid YAML that round-trips.
	cfg, err := config.Load(dest)
	if err != nil {
		t.Fatalf("Load() written config: %v", err)
	}
	if cfg.TicketSource == "" {
		t.Error("loaded config has empty TicketSource")
	}
	if cfg.Mode == "" {
		t.Error("loaded config has empty Mode")
	}

	// Output message must mention the path.
	if !strings.Contains(buf.String(), dest) {
		t.Errorf("output = %q, want to mention %q", buf.String(), dest)
	}
}

func TestRunInit_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "deep", "nested", "soda.yaml")

	if err := runInit(io.Discard, dest, false); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("config not created at nested path: %v", err)
	}
}

func TestRunInit_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	// Write a dummy file.
	if err := os.WriteFile(dest, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	err := runInit(io.Discard, dest, false)
	if err == nil {
		t.Fatal("expected error when file exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "already exists")
	}

	// Original content must be preserved.
	data, _ := os.ReadFile(dest)
	if string(data) != "existing" {
		t.Errorf("file was modified: got %q", string(data))
	}
}

func TestRunInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "soda.yaml")

	// Write a dummy file.
	if err := os.WriteFile(dest, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runInit(io.Discard, dest, true); err != nil {
		t.Fatalf("runInit(force=true) error: %v", err)
	}

	// Content must be replaced with valid config.
	cfg, err := config.Load(dest)
	if err != nil {
		t.Fatalf("Load() after force overwrite: %v", err)
	}
	if cfg.TicketSource == "" {
		t.Error("overwritten config has empty TicketSource")
	}
}

func TestRunInit_StatErrorNotErrNotExist(t *testing.T) {
	dir := t.TempDir()
	// Create a parent dir with no read/execute permission so Stat fails
	// with a permission error, not ErrNotExist.
	noPerms := filepath.Join(dir, "noperm")
	if err := os.Mkdir(noPerms, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(noPerms, 0755) })

	dest := filepath.Join(noPerms, "soda.yaml")
	err := runInit(io.Discard, dest, false)
	if err == nil {
		t.Fatal("expected error for inaccessible path, got nil")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Errorf("error = %q, want stat context", err)
	}
}

func TestResolveInitPath_DefaultPath(t *testing.T) {
	p, err := resolveInitPath("")
	if err != nil {
		t.Fatalf("resolveInitPath(\"\") error: %v", err)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("path %q is not absolute", p)
	}
	if filepath.Base(p) != "soda.yaml" {
		t.Errorf("base = %q, want soda.yaml", filepath.Base(p))
	}
}

func TestResolveInitPath_CustomPath(t *testing.T) {
	p, err := resolveInitPath("my-config.yaml")
	if err != nil {
		t.Fatalf("resolveInitPath() error: %v", err)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("path %q is not absolute", p)
	}
	if filepath.Base(p) != "my-config.yaml" {
		t.Errorf("base = %q, want my-config.yaml", filepath.Base(p))
	}
}
