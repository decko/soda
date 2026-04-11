package pipeline

import (
	"errors"
	"fmt"
	"os"
)

// atomicWrite writes data to path atomically: write to .tmp, fsync, rename.
// Protects against both process crash and power loss.
func atomicWrite(path string, data []byte) error {
	tmpPath := path + ".tmp"

	fd, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("pipeline: create temp file %s: %w", tmpPath, err)
	}

	if _, err := fd.Write(data); err != nil {
		fd.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("pipeline: write temp file %s: %w", tmpPath, err)
	}

	if err := fd.Sync(); err != nil {
		fd.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("pipeline: sync temp file %s: %w", tmpPath, err)
	}

	if err := fd.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("pipeline: close temp file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("pipeline: rename %s to %s: %w", tmpPath, path, err)
	}

	return nil
}

// archiveArtifact renames path to path.<generation> if the file exists.
// Returns nil if the file does not exist (nothing to archive).
func archiveArtifact(path string, generation int) error {
	archivePath := fmt.Sprintf("%s.%d", path, generation)
	err := os.Rename(path, archivePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pipeline: archive %s to %s: %w", path, archivePath, err)
	}
	return nil
}
