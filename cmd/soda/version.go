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
