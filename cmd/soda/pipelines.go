package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newPipelinesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pipelines",
		Short: "List available pipeline configurations",
		Long: `Discover and list pipeline configuration files (phases.yaml and
phases-<name>.yaml) from the working directory, user config directory,
and embedded defaults.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPipelines(cmd)
		},
	}
}

func runPipelines(cmd *cobra.Command) error {
	dirs, sources := discoverDirs()

	pipelines := pipeline.DiscoverPipelines(dirs, sources)

	// Always include the embedded default if not already found.
	hasDefault := false
	for _, p := range pipelines {
		if p.Name == "default" {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		pipelines = append([]pipeline.PipelineInfo{{
			Name:   "default",
			Path:   "(embedded)",
			Source: "embedded",
		}}, pipelines...)
	}

	if len(pipelines) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No pipeline configurations found.")
		return nil
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tPATH")
	for _, p := range pipelines {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, p.Source, p.Path)
	}
	return tw.Flush()
}

// discoverDirs returns the directories and source labels to scan for
// pipeline configuration files. The order matches the resolution
// priority: working directory first, then user config directory.
func discoverDirs() (dirs []string, sources []string) {
	// Working directory.
	cwd, err := os.Getwd()
	if err == nil {
		dirs = append(dirs, cwd)
		sources = append(sources, "local")
	}

	// User config directory.
	configDir, err := os.UserConfigDir()
	if err == nil {
		sodaDir := filepath.Join(configDir, "soda")
		dirs = append(dirs, sodaDir)
		sources = append(sources, "user")
	}

	return dirs, sources
}
