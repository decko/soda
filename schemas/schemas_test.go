package schemas

import (
	"encoding/json"
	"testing"
)

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
		Properties map[string]interface{} `json:"properties"`
		Required   []string               `json:"required"`
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
