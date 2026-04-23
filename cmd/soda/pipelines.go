package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/decko/soda/internal/pipeline"
	"github.com/spf13/cobra"
)

func newPipelinesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipelines",
		Short: "List and manage pipeline configurations",
		Long: `Discover and list pipeline configuration files (phases.yaml and
phases-<name>.yaml) from the working directory, user config directory,
and embedded defaults.

Use "soda pipelines new" to scaffold a new custom pipeline.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPipelines(cmd)
		},
	}

	cmd.AddCommand(newPipelineNewCmd())

	return cmd
}

func runPipelines(cmd *cobra.Command) error {
	dirs, sources := discoverDirs()

	pipelines := pipeline.DiscoverPipelines(dirs, sources)

	// Always include embedded pipelines if not already discovered on disk.
	discovered := make(map[string]bool, len(pipelines))
	for _, p := range pipelines {
		discovered[p.Name] = true
	}

	// Embedded default pipeline.
	if !discovered["default"] {
		pipelines = append([]pipeline.PipelineInfo{{
			Name:   "default",
			Path:   "(embedded)",
			Source: "embedded",
		}}, pipelines...)
	}

	// Known embedded alternative pipelines.
	for name := range knownEmbeddedPipelines {
		if !discovered[name] {
			pipelines = append(pipelines, pipeline.PipelineInfo{
				Name:   name,
				Path:   "(embedded)",
				Source: "embedded",
			})
		}
	}

	// Re-sort: "default" first, then alphabetical.
	sort.Slice(pipelines, func(i, j int) bool {
		if pipelines[i].Name == "default" {
			return true
		}
		if pipelines[j].Name == "default" {
			return false
		}
		return pipelines[i].Name < pipelines[j].Name
	})

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
