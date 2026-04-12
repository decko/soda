package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockInfo holds the PID and acquisition time written to the lock file.
// IsAlive is computed at read time and not serialized.
type LockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
	IsAlive    bool      `json:"-"`
}

// acquireLock acquires an exclusive flock on the lock file in dir.
// Returns the open file descriptor (caller must hold it to maintain the lock).
func acquireLock(dir string) (*os.File, error) {
	lockPath := filepath.Join(dir, "lock")

	fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", lockPath, err)
	}

	err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		writeLockInfo(fd)
		return fd, nil
	}

	if err != syscall.EWOULDBLOCK {
		fd.Close()
		return nil, fmt.Errorf("flock %s: %w", lockPath, err)
	}

	// Lock is held — check if the holder is alive
	info, readErr := readLockInfo(fd)
	if readErr == nil && !isPIDAlive(info.PID) {
		// Stale lock: holder is dead. Retry — kernel should have released flock.
		err = syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			writeLockInfo(fd)
			return fd, nil
		}
	}

	fd.Close()
	if info != nil && info.PID > 0 {
		return nil, fmt.Errorf("locked by PID %d (acquired %s)",
			info.PID, info.AcquiredAt.Format(time.RFC3339))
	}
	return nil, fmt.Errorf("lock %s is held by another process", lockPath)
}

// releaseLock releases the flock and closes the file descriptor.
func releaseLock(fd *os.File) {
	if fd == nil {
		return
	}
	syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	fd.Close()
}

// writeLockInfo truncates and writes PID + timestamp to the lock file.
func writeLockInfo(fd *os.File) {
	info := LockInfo{
		PID:        os.Getpid(),
		AcquiredAt: time.Now(),
	}
	data, _ := json.Marshal(info)
	fd.Truncate(0)
	fd.Seek(0, 0)
	fd.Write(data)
	fd.Sync()
}

// readLockInfo reads and parses the lock file contents.
func readLockInfo(fd *os.File) (*LockInfo, error) {
	fd.Seek(0, 0)
	data := make([]byte, 1024)
	n, err := fd.Read(data)
	if err != nil || n == 0 {
		if err == nil {
			err = fmt.Errorf("empty lock file")
		}
		return nil, fmt.Errorf("pipeline: read lock info: %w", err)
	}
	var info LockInfo
	if err := json.Unmarshal(data[:n], &info); err != nil {
		return nil, fmt.Errorf("pipeline: parse lock info: %w", err)
	}
	return &info, nil
}

// ReadLockInfo reads and parses a lock file from a path, computing IsAlive.
func ReadLockInfo(lockPath string) (*LockInfo, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("pipeline: read lock %s: %w", lockPath, err)
	}
	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("pipeline: parse lock %s: %w", lockPath, err)
	}
	info.IsAlive = isPIDAlive(info.PID)
	return &info, nil
}

// isPIDAlive checks if a process with the given PID is running.
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
