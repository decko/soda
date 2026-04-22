package schemas

// SchemaFor returns the generated JSON Schema string for the named phase.
// Returns an empty string if the phase is not recognized.
func SchemaFor(phase string) string {
	schema, ok := phaseSchemas[phase]
	if !ok {
		return ""
	}
	return schema
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
