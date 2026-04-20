package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print detailed version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(buildVersionString())
		},
	}
}

func buildVersionString() string {
	goVersion := "(unknown)"
	commit := "(unknown)"

	info, ok := debug.ReadBuildInfo()
	if ok {
		goVersion = info.GoVersion
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				commit = s.Value[:12]
				break
			} else if s.Key == "vcs.revision" {
				commit = s.Value
				break
			}
		}
	}

	return fmt.Sprintf("soda version %s (%s, commit %s)", version, goVersion, commit)
}

// binaryVersionID returns a compact identifier for the current binary build.
// It combines the linker-set version with the VCS commit hash (from
// debug.ReadBuildInfo) so that any rebuild — even at the same version tag —
// produces a different identifier. When VCS info is unavailable (e.g. go run),
// only the version string is returned.
func binaryVersionID() string {
	commit := ""
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				if len(s.Value) >= 12 {
					commit = s.Value[:12]
				} else {
					commit = s.Value
				}
				break
			}
		}
	}

	if commit != "" {
		return version + "-" + commit
	}
	return version
}
