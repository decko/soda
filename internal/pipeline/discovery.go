package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PipelineInfo describes a discovered pipeline configuration file.
type PipelineInfo struct {
	Name   string // pipeline name; "default" for phases.yaml
	Path   string // absolute or relative path to the file
	Source string // "local", "user", or "embedded"
}

// PipelineFilename returns the expected filename for a pipeline name.
// The default pipeline maps to "phases.yaml"; named pipelines map to
// "phases-<name>.yaml".
func PipelineFilename(name string) string {
	if name == "" || name == "default" {
		return "phases.yaml"
	}
	return fmt.Sprintf("phases-%s.yaml", name)
}

// PipelineNameFromFile extracts the pipeline name from a filename.
// "phases.yaml" returns "default"; "phases-foo.yaml" returns "foo".
// Returns empty string if the filename doesn't match the convention.
func PipelineNameFromFile(filename string) string {
	base := filepath.Base(filename)
	if base == "phases.yaml" {
		return "default"
	}
	if strings.HasPrefix(base, "phases-") && strings.HasSuffix(base, ".yaml") {
		name := strings.TrimPrefix(base, "phases-")
		name = strings.TrimSuffix(name, ".yaml")
		if name != "" {
			return name
		}
	}
	return ""
}

// DiscoverPipelines scans directories for pipeline configuration files.
// It looks for phases.yaml (name="default") and phases-<name>.yaml patterns.
// Each directory is labeled with the corresponding source tag. Directories
// are scanned in the order provided; when a pipeline name appears in
// multiple directories, only the first occurrence is kept (higher-priority
// source wins). The returned slice is sorted alphabetically by name, with
// "default" always first.
func DiscoverPipelines(dirs []string, sources []string) []PipelineInfo {
	if len(dirs) != len(sources) {
		return nil
	}

	seen := make(map[string]bool)
	var pipelines []PipelineInfo

	for idx, dir := range dirs {
		source := sources[idx]

		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := PipelineNameFromFile(entry.Name())
			if name == "" {
				continue
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			pipelines = append(pipelines, PipelineInfo{
				Name:   name,
				Path:   filepath.Join(dir, entry.Name()),
				Source: source,
			})
		}
	}

	sort.Slice(pipelines, func(i, j int) bool {
		// "default" always first.
		if pipelines[i].Name == "default" {
			return true
		}
		if pipelines[j].Name == "default" {
			return false
		}
		return pipelines[i].Name < pipelines[j].Name
	})

	return pipelines
}
