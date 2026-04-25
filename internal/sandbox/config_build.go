package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// sandboxPaths holds the computed read and write paths for a sandbox profile.
type sandboxPaths struct {
	ReadPaths  []string
	WritePaths []string
}

// buildSandboxPaths assembles the sandbox read/write path lists from the
// worktree directory, temp directory, claude binary read paths, and any
// extra paths from the Config. The result is used to populate arapuca.Profile.
func buildSandboxPaths(workDir, tmpDir string, claudeRead, extraRead, extraWrite []string) sandboxPaths {
	readPaths := systemReadPaths()
	readPaths = append(readPaths, claudeRead...)
	readPaths = append(readPaths, workDir)
	readPaths = append(readPaths, extraRead...)

	// Allow SSH agent socket access for git push.
	if sshSock := os.Getenv("SSH_AUTH_SOCK"); sshSock != "" {
		readPaths = append(readPaths, filepath.Dir(sshSock))
	}

	writePaths := []string{workDir}
	writePaths = append(writePaths, extraWrite...)

	// Temp dir needs both read and write access.
	readPaths = append(readPaths, tmpDir)
	writePaths = append(writePaths, tmpDir)

	return sandboxPaths{ReadPaths: readPaths, WritePaths: writePaths}
}

// buildProxyURL formats a proxy base URL from a listener address string
// (e.g. "127.0.0.1:43210" → "http://127.0.0.1:43210").
func buildProxyURL(addr string) string {
	return fmt.Sprintf("http://%s", addr)
}
