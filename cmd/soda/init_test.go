package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decko/soda/internal/config"
)

func TestRunInit_WritesDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "config.yaml")

	if err := runInit(dest, false); err != nil {
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
}

func TestRunInit_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "deep", "nested", "config.yaml")

	if err := runInit(dest, false); err != nil {
		t.Fatalf("runInit() error: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("config not created at nested path: %v", err)
	}
}

func TestRunInit_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "config.yaml")

	// Write a dummy file.
	if err := os.WriteFile(dest, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	err := runInit(dest, false)
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
	dest := filepath.Join(dir, "config.yaml")

	// Write a dummy file.
	if err := os.WriteFile(dest, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runInit(dest, true); err != nil {
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

func TestResolveInitPath_DefaultPath(t *testing.T) {
	p, err := resolveInitPath("")
	if err != nil {
		t.Fatalf("resolveInitPath(\"\") error: %v", err)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("path %q is not absolute", p)
	}
	if filepath.Base(p) != "config.yaml" {
		t.Errorf("base = %q, want config.yaml", filepath.Base(p))
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
