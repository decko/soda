package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	t.Run("writes_file_atomically", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.json")

		if err := atomicWrite(path, []byte(`{"key":"value"}`)); err != nil {
			t.Fatalf("atomicWrite: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != `{"key":"value"}` {
			t.Errorf("content = %q, want %q", got, `{"key":"value"}`)
		}

		// Verify no .tmp file left behind
		tmpPath := path + ".tmp"
		if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temp file should not exist, got err: %v", err)
		}
	})

	t.Run("overwrites_existing_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.json")

		if err := atomicWrite(path, []byte("first")); err != nil {
			t.Fatalf("first write: %v", err)
		}
		if err := atomicWrite(path, []byte("second")); err != nil {
			t.Fatalf("second write: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "second" {
			t.Errorf("content = %q, want %q", got, "second")
		}
	})

	t.Run("orphaned_tmp_does_not_corrupt", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.json")

		// Write the real file
		if err := atomicWrite(path, []byte("real")); err != nil {
			t.Fatalf("atomicWrite: %v", err)
		}

		// Simulate a crash: leave an orphaned .tmp file
		tmpPath := path + ".tmp"
		if err := os.WriteFile(tmpPath, []byte("orphaned"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		// A new atomicWrite should overwrite the orphaned .tmp and succeed
		if err := atomicWrite(path, []byte("updated")); err != nil {
			t.Fatalf("atomicWrite after orphan: %v", err)
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "updated" {
			t.Errorf("content = %q, want %q", got, "updated")
		}
	})
}

func TestArchiveArtifact(t *testing.T) {
	t.Run("renames_existing_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "verify.json")

		if err := os.WriteFile(path, []byte("gen1"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := archiveArtifact(path, 1); err != nil {
			t.Fatalf("archiveArtifact: %v", err)
		}

		// Original should be gone
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Error("original file should not exist after archive")
		}

		// Archived file should exist
		archived, err := os.ReadFile(path + ".1")
		if err != nil {
			t.Fatalf("archived file: %v", err)
		}
		if string(archived) != "gen1" {
			t.Errorf("archived content = %q, want %q", archived, "gen1")
		}
	})

	t.Run("noop_when_file_missing", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "nonexistent.json")

		if err := archiveArtifact(path, 1); err != nil {
			t.Fatalf("archiveArtifact on missing file: %v", err)
		}
	})

	t.Run("multiple_generations", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "verify.json")

		// Create and archive generation 1
		if err := os.WriteFile(path, []byte("gen1"), 0644); err != nil {
			t.Fatalf("WriteFile gen1: %v", err)
		}
		archiveArtifact(path, 1)

		// Create and archive generation 2
		if err := os.WriteFile(path, []byte("gen2"), 0644); err != nil {
			t.Fatalf("WriteFile gen2: %v", err)
		}
		archiveArtifact(path, 2)

		// Both archives should exist with correct content
		got1, _ := os.ReadFile(path + ".1")
		got2, _ := os.ReadFile(path + ".2")
		if string(got1) != "gen1" {
			t.Errorf("gen1 content = %q", got1)
		}
		if string(got2) != "gen2" {
			t.Errorf("gen2 content = %q", got2)
		}
	})
}
