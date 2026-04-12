package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAcquireLock(t *testing.T) {
	t.Run("acquire_and_release", func(t *testing.T) {
		dir := t.TempDir()

		fd, err := acquireLock(dir)
		if err != nil {
			t.Fatalf("acquireLock: %v", err)
		}
		if fd == nil {
			t.Fatal("expected non-nil fd")
		}

		// Lock file should exist with PID info
		info, err := readLockInfo(fd)
		if err != nil {
			t.Fatalf("readLockInfo: %v", err)
		}
		if info.PID != os.Getpid() {
			t.Errorf("PID = %d, want %d", info.PID, os.Getpid())
		}

		releaseLock(fd)

		// Should be able to acquire again after release
		fd2, err := acquireLock(dir)
		if err != nil {
			t.Fatalf("re-acquire after release: %v", err)
		}
		releaseLock(fd2)
	})

	t.Run("contention_returns_error", func(t *testing.T) {
		dir := t.TempDir()

		fd1, err := acquireLock(dir)
		if err != nil {
			t.Fatalf("first acquireLock: %v", err)
		}
		defer releaseLock(fd1)

		// Second acquire from same process (different fd) should fail
		_, err = acquireLock(dir)
		if err == nil {
			t.Fatal("expected error for contention")
		}
		if !strings.Contains(err.Error(), "locked by PID") {
			t.Errorf("error should mention PID, got: %v", err)
		}
	})

	t.Run("release_nil_is_noop", func(t *testing.T) {
		releaseLock(nil) // should not panic
	})
}

func TestIsPIDAlive(t *testing.T) {
	t.Run("current_process_is_alive", func(t *testing.T) {
		if !isPIDAlive(os.Getpid()) {
			t.Error("current process should be alive")
		}
	})

	t.Run("invalid_pid_is_not_alive", func(t *testing.T) {
		if isPIDAlive(0) {
			t.Error("PID 0 should not be considered alive")
		}
		if isPIDAlive(-1) {
			t.Error("PID -1 should not be considered alive")
		}
	})

	t.Run("nonexistent_pid_is_not_alive", func(t *testing.T) {
		// PID 4194304 is above Linux's default max PID (4194304 is the kernel max)
		// Use a very high but valid PID that is almost certainly unused
		if isPIDAlive(4194300) {
			t.Skip("PID 4194300 unexpectedly exists")
		}
	})
}

func TestStaleDetection(t *testing.T) {
	t.Run("stale_lock_file_with_no_flock", func(t *testing.T) {
		dir := t.TempDir()

		// Simulate a stale lock: write lock file with a dead PID but no flock held.
		// This simulates a process that crashed after writing the lock file.
		lockPath := dir + "/lock"
		os.WriteFile(lockPath, []byte(`{"pid":4194300,"acquired_at":"2026-01-01T00:00:00Z"}`), 0644)

		// acquireLock should succeed because no flock is actually held
		fd, err := acquireLock(dir)
		if err != nil {
			t.Fatalf("acquireLock on stale lock: %v", err)
		}
		defer releaseLock(fd)

		// Lock file should now have our PID
		info, _ := readLockInfo(fd)
		if info.PID != os.Getpid() {
			t.Errorf("PID = %d, want %d", info.PID, os.Getpid())
		}
	})
}

func TestAcquireLock_AfterHolderCloses(t *testing.T) {
	dir := t.TempDir()

	// Simulate holder: open and flock, then close (simulating crash)
	lockPath := dir + "/lock"
	holderFd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(holderFd.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	holderFd.Write([]byte(`{"pid":99999,"acquired_at":"2026-01-01T00:00:00Z"}`))
	holderFd.Close() // simulates crash -- kernel releases flock

	// Now acquireLock should succeed
	fd, err := acquireLock(dir)
	if err != nil {
		t.Fatalf("acquireLock after holder closed: %v", err)
	}
	releaseLock(fd)
}

func TestReadLockInfo(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")

	info := LockInfo{
		PID:        os.Getpid(),
		AcquiredAt: time.Now().Truncate(time.Second),
	}
	data, _ := json.Marshal(info)
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	got, err := ReadLockInfo(lockPath)
	if err != nil {
		t.Fatalf("ReadLockInfo: %v", err)
	}
	if got.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", got.PID, os.Getpid())
	}
	if !got.IsAlive {
		t.Error("IsAlive = false, want true (current process)")
	}

	// Write a dead PID
	info.PID = 999999999
	data, _ = json.Marshal(info)
	os.WriteFile(lockPath, data, 0644)

	got, err = ReadLockInfo(lockPath)
	if err != nil {
		t.Fatalf("ReadLockInfo (dead pid): %v", err)
	}
	if got.IsAlive {
		t.Error("IsAlive = true for dead PID, want false")
	}

	// Missing file
	_, err = ReadLockInfo(filepath.Join(dir, "nonexistent"))
	if err == nil {
		t.Error("expected error for missing lock file")
	}
}
