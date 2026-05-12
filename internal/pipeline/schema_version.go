package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/decko/soda/schemas"
)

// checkSchemaVersions validates that completed phases preceding fromPhase
// have artifacts whose _schema_version matches the current schema hash.
//
// Phases with missing _schema_version (old artifacts) emit a warning event
// but do not block. Phases with a mismatched hash emit an error event and
// return a SchemaVersionMismatchError — unless Force is true, in which case
// only a warning event is emitted.
func (e *Engine) checkSchemaVersions(fromPhase string) error {
	phases := e.config.Pipeline.Phases

	for _, phase := range phases {
		if phase.Name == fromPhase {
			break
		}

		if !e.state.IsCompleted(phase.Name) {
			continue
		}

		currentVersion := schemas.SchemaVersionFor(phase.Name)
		if currentVersion == "" {
			// Phase has no known schema — nothing to validate.
			continue
		}

		storedVersion, err := extractSchemaVersion(e.state, phase.Name)
		if err != nil {
			// Result file missing or unreadable — skip silently.
			continue
		}

		if storedVersion == "" {
			// Old artifact without _schema_version — warn but don't block.
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventSchemaVersionMismatch,
				Data: map[string]any{
					"stored_version":  "",
					"current_version": currentVersion,
					"warning":         "artifact has no schema version (old format)",
				},
			})
			continue
		}

		if storedVersion != currentVersion {
			e.emit(Event{
				Phase: phase.Name,
				Kind:  EventSchemaVersionMismatch,
				Data: map[string]any{
					"stored_version":  storedVersion,
					"current_version": currentVersion,
				},
			})

			if !e.config.Force {
				return &SchemaVersionMismatchError{
					Phase:          phase.Name,
					StoredVersion:  storedVersion,
					CurrentVersion: currentVersion,
				}
			}
		}
	}

	return nil
}

// extractSchemaVersion reads the _schema_version field from a phase's
// stored result JSON. Returns ("", nil) if the field is absent. Returns
// an error if the result cannot be read or is not a JSON object.
func extractSchemaVersion(state *State, phase string) (string, error) {
	data, err := state.ReadResult(phase)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("result not found")
		}
		return "", fmt.Errorf("read result: %w", err)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return "", fmt.Errorf("unmarshal result: %w", err)
	}

	versionRaw, ok := obj["_schema_version"]
	if !ok {
		return "", nil
	}

	var version string
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return "", fmt.Errorf("unmarshal _schema_version: %w", err)
	}

	return version, nil
}
