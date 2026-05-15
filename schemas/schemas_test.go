package schemas

import (
	"encoding/json"
	"testing"
)

func TestSchemaVersionFor_KnownPhase(t *testing.T) {
	version := SchemaVersionFor("triage")
	if version == "" {
		t.Fatal("SchemaVersionFor(triage) returned empty string")
	}
	if len(version) != 16 {
		t.Errorf("SchemaVersionFor(triage) length = %d, want 16", len(version))
	}
	// Must be valid hex characters.
	for _, ch := range version {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Errorf("SchemaVersionFor(triage) contains non-hex char %q", ch)
		}
	}
}

func TestSchemaVersionFor_StableAcrossCalls(t *testing.T) {
	first := SchemaVersionFor("plan")
	second := SchemaVersionFor("plan")
	if first != second {
		t.Errorf("SchemaVersionFor not stable: %q != %q", first, second)
	}
}

func TestSchemaVersionFor_DifferentPhasesDiffer(t *testing.T) {
	triageVersion := SchemaVersionFor("triage")
	planVersion := SchemaVersionFor("plan")
	if triageVersion == planVersion {
		t.Errorf("triage and plan should have different schema versions, both = %q", triageVersion)
	}
}

func TestSchemaVersionFor_UnknownPhase(t *testing.T) {
	version := SchemaVersionFor("nonexistent")
	if version != "" {
		t.Errorf("SchemaVersionFor(nonexistent) = %q, want empty", version)
	}
}

func TestSchemaFor_KnownPhases(t *testing.T) {
	phases := []struct {
		name    string
		wantVar string
	}{
		{"triage", TriageSchema},
		{"plan", PlanSchema},
		{"implement", ImplementSchema},
		{"verify", VerifySchema},
		{"review", ReviewSchema},
		{"submit", SubmitSchema},
		{"patch", PatchSchema},
		{"monitor", MonitorSchema},
	}

	for _, tt := range phases {
		t.Run(tt.name, func(t *testing.T) {
			got := SchemaFor(tt.name)
			if got == "" {
				t.Fatalf("SchemaFor(%q) returned empty string", tt.name)
			}
			if got != tt.wantVar {
				t.Errorf("SchemaFor(%q) = %q, want %q", tt.name, got, tt.wantVar)
			}
		})
	}
}

func TestSchemaFor_UnknownPhase(t *testing.T) {
	got := SchemaFor("nonexistent")
	if got != "" {
		t.Errorf("SchemaFor(nonexistent) = %q, want empty", got)
	}
}

func TestSchemaFor_EmptyPhase(t *testing.T) {
	got := SchemaFor("")
	if got != "" {
		t.Errorf("SchemaFor(\"\") = %q, want empty", got)
	}
}

func TestGeneratedSchemas_ValidJSON(t *testing.T) {
	schemas := map[string]string{
		"triage":    TriageSchema,
		"plan":      PlanSchema,
		"implement": ImplementSchema,
		"verify":    VerifySchema,
		"review":    ReviewSchema,
		"submit":    SubmitSchema,
		"patch":     PatchSchema,
		"monitor":   MonitorSchema,
	}

	for name, schema := range schemas {
		t.Run(name, func(t *testing.T) {
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
				t.Fatalf("schema is not valid JSON: %v", err)
			}

			// Verify it's a JSON Schema object.
			typ, ok := parsed["type"]
			if !ok {
				t.Fatal("schema missing 'type' field")
			}
			if typ != "object" {
				t.Errorf("schema type = %q, want 'object'", typ)
			}

			// Verify it has properties.
			props, ok := parsed["properties"]
			if !ok {
				t.Fatal("schema missing 'properties' field")
			}
			propsMap, ok := props.(map[string]interface{})
			if !ok {
				t.Fatal("properties is not an object")
			}
			if len(propsMap) == 0 {
				t.Error("properties is empty")
			}

			// Verify ticket_key is always present.
			if _, ok := propsMap["ticket_key"]; !ok {
				t.Error("schema missing 'ticket_key' property")
			}
		})
	}
}

func TestTriageSchema_MatchesStruct(t *testing.T) {
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal([]byte(TriageSchema), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// TriageOutput has these non-omitempty fields.
	wantRequired := []string{
		"approach", "automatable", "code_area", "complexity",
		"files", "repo", "risks", "ticket_key",
	}
	if len(parsed.Required) != len(wantRequired) {
		t.Fatalf("required count = %d, want %d: %v", len(parsed.Required), len(wantRequired), parsed.Required)
	}
	reqSet := make(map[string]bool)
	for _, field := range parsed.Required {
		reqSet[field] = true
	}
	for _, want := range wantRequired {
		if !reqSet[want] {
			t.Errorf("required missing %q", want)
		}
	}

	// block_reason and skip_plan are omitempty, so NOT required.
	for _, field := range []string{"block_reason", "skip_plan"} {
		if reqSet[field] {
			t.Errorf("%q should not be required (omitempty)", field)
		}
	}

	// All properties should exist.
	wantProps := []string{
		"ticket_key", "repo", "code_area", "files", "complexity",
		"approach", "risks", "automatable", "block_reason", "skip_plan",
	}
	for _, prop := range wantProps {
		if _, ok := parsed.Properties[prop]; !ok {
			t.Errorf("missing property %q", prop)
		}
	}

	// automatable should be type=string with enum constraint (not boolean).
	assertEnum(t, parsed.Properties["automatable"], "string", []string{"yes", "no", "partial"})

	// complexity should have enum constraint.
	assertEnum(t, parsed.Properties["complexity"], "string", []string{"low", "medium", "high"})
}

func TestVerifySchema_VerdictEnum(t *testing.T) {
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(VerifySchema), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertEnum(t, parsed.Properties["verdict"], "string", []string{"PASS", "FAIL"})
}

func TestReviewSchema_VerdictEnum(t *testing.T) {
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(ReviewSchema), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertEnum(t, parsed.Properties["verdict"], "string", []string{"pass", "rework", "pass-with-follow-ups"})
}

// assertEnum verifies that a JSON Schema property has the expected type and enum values.
func assertEnum(t *testing.T, raw json.RawMessage, wantType string, wantEnum []string) {
	t.Helper()
	var prop struct {
		Type string   `json:"type"`
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(raw, &prop); err != nil {
		t.Fatalf("unmarshal property: %v", err)
	}
	if prop.Type != wantType {
		t.Errorf("type = %q, want %q", prop.Type, wantType)
	}
	if len(prop.Enum) != len(wantEnum) {
		t.Fatalf("enum count = %d, want %d: %v", len(prop.Enum), len(wantEnum), prop.Enum)
	}
	for i, v := range wantEnum {
		if prop.Enum[i] != v {
			t.Errorf("enum[%d] = %q, want %q", i, prop.Enum[i], v)
		}
	}
}

func TestPatchSchema_MatchesStruct(t *testing.T) {
	var parsed struct {
		Properties map[string]interface{} `json:"properties"`
		Required   []string               `json:"required"`
	}
	if err := json.Unmarshal([]byte(PatchSchema), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// PatchOutput has these non-omitempty fields.
	wantRequired := []string{
		"files_changed", "fix_results", "tests_passed", "ticket_key", "too_complex",
	}
	if len(parsed.Required) != len(wantRequired) {
		t.Fatalf("required count = %d, want %d: %v", len(parsed.Required), len(wantRequired), parsed.Required)
	}
	reqSet := make(map[string]bool)
	for _, field := range parsed.Required {
		reqSet[field] = true
	}
	for _, want := range wantRequired {
		if !reqSet[want] {
			t.Errorf("required missing %q", want)
		}
	}

	// too_complex_reason is omitempty, so NOT required.
	if reqSet["too_complex_reason"] {
		t.Error("too_complex_reason should not be required (omitempty)")
	}

	// All properties should exist.
	wantProps := []string{
		"ticket_key", "fix_results", "files_changed", "tests_passed",
		"too_complex", "too_complex_reason",
	}
	for _, prop := range wantProps {
		if _, ok := parsed.Properties[prop]; !ok {
			t.Errorf("missing property %q", prop)
		}
	}
}

func TestReviewSchema_MatchesStruct(t *testing.T) {
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal([]byte(ReviewSchema), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// ReviewOutput has these non-omitempty fields.
	wantRequired := []string{"findings", "ticket_key", "verdict"}
	if len(parsed.Required) != len(wantRequired) {
		t.Fatalf("required count = %d, want %d: %v", len(parsed.Required), len(wantRequired), parsed.Required)
	}
	reqSet := make(map[string]bool)
	for _, field := range parsed.Required {
		reqSet[field] = true
	}
	for _, want := range wantRequired {
		if !reqSet[want] {
			t.Errorf("required missing %q", want)
		}
	}

	// All top-level properties should exist.
	wantProps := []string{"findings", "ticket_key", "verdict"}
	for _, prop := range wantProps {
		if _, ok := parsed.Properties[prop]; !ok {
			t.Errorf("missing property %q", prop)
		}
	}

	// Drill into findings array items to validate ReviewFinding schema.
	findingsRaw, ok := parsed.Properties["findings"]
	if !ok {
		t.Fatal("missing 'findings' property")
	}

	var findings struct {
		Type  string `json:"type"`
		Items struct {
			Type       string                 `json:"type"`
			Properties map[string]interface{} `json:"properties"`
			Required   []string               `json:"required"`
		} `json:"items"`
	}
	if err := json.Unmarshal(findingsRaw, &findings); err != nil {
		t.Fatalf("unmarshal findings: %v", err)
	}

	if findings.Type != "array" {
		t.Errorf("findings.type = %q, want 'array'", findings.Type)
	}
	if findings.Items.Type != "object" {
		t.Errorf("findings.items.type = %q, want 'object'", findings.Items.Type)
	}

	// ReviewFinding non-omitempty fields are required.
	wantItemRequired := []string{"file", "issue", "severity", "suggestion"}
	if len(findings.Items.Required) != len(wantItemRequired) {
		t.Fatalf("findings.items required count = %d, want %d: %v",
			len(findings.Items.Required), len(wantItemRequired), findings.Items.Required)
	}
	itemReqSet := make(map[string]bool)
	for _, field := range findings.Items.Required {
		itemReqSet[field] = true
	}
	for _, want := range wantItemRequired {
		if !itemReqSet[want] {
			t.Errorf("findings.items required missing %q", want)
		}
	}

	// source, line, and category are omitempty, so NOT required.
	for _, field := range []string{"source", "line", "category"} {
		if itemReqSet[field] {
			t.Errorf("findings.items: %q should not be required (omitempty)", field)
		}
	}

	// All ReviewFinding properties should exist.
	wantItemProps := []string{"category", "file", "issue", "line", "severity", "source", "suggestion"}
	for _, prop := range wantItemProps {
		if _, ok := findings.Items.Properties[prop]; !ok {
			t.Errorf("findings.items missing property %q", prop)
		}
	}

	// category should have enum constraint.
	var itemProps map[string]json.RawMessage
	if err := json.Unmarshal(findingsRaw, &struct {
		Items struct {
			Properties *map[string]json.RawMessage `json:"properties"`
		} `json:"items"`
	}{Items: struct {
		Properties *map[string]json.RawMessage `json:"properties"`
	}{Properties: &itemProps}}); err != nil {
		t.Fatalf("unmarshal item properties: %v", err)
	}
	assertEnum(t, itemProps["category"], "string", []string{
		"retrieval", "convention", "logic", "test_pattern", "documentation",
	})
}

func TestImplementSchema_NestedTypes(t *testing.T) {
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(ImplementSchema), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// commits should be an array of objects.
	commitsRaw, ok := parsed.Properties["commits"]
	if !ok {
		t.Fatal("missing 'commits' property")
	}

	var commits struct {
		Type  string `json:"type"`
		Items struct {
			Type       string                 `json:"type"`
			Properties map[string]interface{} `json:"properties"`
			Required   []string               `json:"required"`
		} `json:"items"`
	}
	if err := json.Unmarshal(commitsRaw, &commits); err != nil {
		t.Fatalf("unmarshal commits: %v", err)
	}

	if commits.Type != "array" {
		t.Errorf("commits.type = %q, want 'array'", commits.Type)
	}
	if commits.Items.Type != "object" {
		t.Errorf("commits.items.type = %q, want 'object'", commits.Items.Type)
	}

	// CommitRecord has hash, message, task_id — all required.
	wantFields := []string{"hash", "message", "task_id"}
	for _, field := range wantFields {
		if _, ok := commits.Items.Properties[field]; !ok {
			t.Errorf("commits.items missing property %q", field)
		}
	}
}
