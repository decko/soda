package schemas

import (
	"crypto/sha256"
	"encoding/hex"
)

// SchemaFor returns the generated JSON Schema string for the named phase.
// Returns an empty string if the phase is not recognized.
func SchemaFor(phase string) string {
	schema, ok := phaseSchemas[phase]
	if !ok {
		return ""
	}
	return schema
}

// SchemaVersionFor returns a content-addressed version hash for the named
// phase's schema. The hash is the first 8 bytes (16 hex characters) of the
// SHA-256 digest of the schema content. Returns an empty string if the phase
// is not recognized.
func SchemaVersionFor(phase string) string {
	schema := SchemaFor(phase)
	if schema == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(schema))
	return hex.EncodeToString(digest[:8])
}

// phaseSchemas maps phase names to their generated JSON Schema constants.
var phaseSchemas = map[string]string{
	"triage":    TriageSchema,
	"plan":      PlanSchema,
	"implement": ImplementSchema,
	"verify":    VerifySchema,
	"review":    ReviewSchema,
	"submit":    SubmitSchema,
	"follow-up": FollowUpSchema,
	"patch":     PatchSchema,
	"monitor":   MonitorSchema,
	"spec":      SpecSchema,
}
