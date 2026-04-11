package claude

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantErr     bool
		errType     string // "parse", "semantic"
		checkResult func(t *testing.T, r *RunResult)
	}{
		{
			name:    "success",
			fixture: "testdata/success.json",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.CostUSD != 0.097 {
					t.Errorf("CostUSD = %v, want 0.097", r.CostUSD)
				}
				if r.Tokens.InputTokens != 1174 {
					t.Errorf("InputTokens = %d, want 1174", r.Tokens.InputTokens)
				}
				if r.Tokens.OutputTokens != 1020 {
					t.Errorf("OutputTokens = %d, want 1020", r.Tokens.OutputTokens)
				}
				if r.Tokens.CacheCreationInputTokens != 7493 {
					t.Errorf("CacheCreationInputTokens = %d, want 7493", r.Tokens.CacheCreationInputTokens)
				}
				if r.Tokens.CacheReadInputTokens != 38034 {
					t.Errorf("CacheReadInputTokens = %d, want 38034", r.Tokens.CacheReadInputTokens)
				}
				if r.Turns != 10 {
					t.Errorf("Turns = %d, want 10", r.Turns)
				}
				if r.Duration != 29689*time.Millisecond {
					t.Errorf("Duration = %v, want %v", r.Duration, 29689*time.Millisecond)
				}
				if r.Result != "Implementation complete." {
					t.Errorf("Result = %q", r.Result)
				}
				var so map[string]interface{}
				if err := json.Unmarshal(r.Output, &so); err != nil {
					t.Fatalf("Output unmarshal: %v", err)
				}
				if so["ticket_key"] != "PROJ-123" {
					t.Errorf("ticket_key = %v, want PROJ-123", so["ticket_key"])
				}
			},
		},
		{
			name:    "error_response",
			fixture: "testdata/error_response.json",
			wantErr: true,
			errType: "semantic",
		},
		{
			name:    "missing_usage",
			fixture: "testdata/missing_usage.json",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.Tokens.InputTokens != 0 {
					t.Errorf("InputTokens = %d, want 0", r.Tokens.InputTokens)
				}
				if r.CostUSD != 0.01 {
					t.Errorf("CostUSD = %v, want 0.01", r.CostUSD)
				}
			},
		},
		{
			name:    "extra_fields",
			fixture: "testdata/extra_fields.json",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.CostUSD != 0.01 {
					t.Errorf("CostUSD = %v, want 0.01", r.CostUSD)
				}
			},
		},
		{
			name:    "no_structured_output",
			fixture: "testdata/no_structured_output.json",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.Output != nil {
					t.Errorf("Output = %s, want nil", r.Output)
				}
				if r.Result != "No structured output was produced." {
					t.Errorf("Result = %q", r.Result)
				}
			},
		},
		{
			name:    "empty_output",
			fixture: "testdata/empty_output.json",
			wantErr: true,
			errType: "parse",
		},
		{
			name:    "mixed_streaming",
			fixture: "testdata/mixed_streaming.txt",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				if r.Result != "Tests pass." {
					t.Errorf("Result = %q, want %q", r.Result, "Tests pass.")
				}
				if r.CostUSD != 0.05 {
					t.Errorf("CostUSD = %v, want 0.05", r.CostUSD)
				}
			},
		},
		{
			name:    "wrong_type",
			fixture: "testdata/wrong_type.json",
			wantErr: true,
			errType: "parse",
		},
		{
			name:    "fake_envelope_in_tool_output",
			fixture: "testdata/fake_envelope_in_tool_output.txt",
			checkResult: func(t *testing.T, r *RunResult) {
				t.Helper()
				var so map[string]interface{}
				if err := json.Unmarshal(r.Output, &so); err != nil {
					t.Fatalf("Output unmarshal: %v", err)
				}
				if so["real"] != true {
					t.Errorf("Expected real output, got %v", so)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(tt.fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			result, err := ParseResponse(data)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				switch tt.errType {
				case "parse":
					var pe *ParseError
					if !errors.As(err, &pe) {
						t.Errorf("expected ParseError, got %T: %v", err, err)
					}
				case "semantic":
					var se *SemanticError
					if !errors.As(err, &se) {
						t.Errorf("expected SemanticError, got %T: %v", err, err)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, raw []byte)
	}{
		{
			name:  "single_line_json",
			input: `{"type":"result","subtype":"success"}`,
			check: func(t *testing.T, raw []byte) {
				if string(raw) != `{"type":"result","subtype":"success"}` {
					t.Errorf("got %s", raw)
				}
			},
		},
		{
			name:  "json_after_text",
			input: "some text\n" + `{"type":"result"}`,
			check: func(t *testing.T, raw []byte) {
				if string(raw) != `{"type":"result"}` {
					t.Errorf("got %s", raw)
				}
			},
		},
		{
			name:    "no_json",
			input:   "just plain text\nno json here\n",
			wantErr: true,
		},
		{
			name:    "empty_input",
			input:   "",
			wantErr: true,
		},
		{
			name:  "trailing_whitespace",
			input: `{"type":"result"}` + "\n\n  \n",
			check: func(t *testing.T, raw []byte) {
				if string(raw) != `{"type":"result"}` {
					t.Errorf("got %s", raw)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := extractJSON([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, raw)
			}
		})
	}
}

func TestParseTokenUsage(t *testing.T) {
	t.Run("known_fields", func(t *testing.T) {
		raw := []byte(`{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":200}`)
		usage := parseTokenUsage(raw)
		if usage.InputTokens != 100 {
			t.Errorf("InputTokens = %d", usage.InputTokens)
		}
		if usage.OutputTokens != 50 {
			t.Errorf("OutputTokens = %d", usage.OutputTokens)
		}
		if usage.CacheReadInputTokens != 200 {
			t.Errorf("CacheReadInputTokens = %d", usage.CacheReadInputTokens)
		}
	})

	t.Run("extra_fields_collected", func(t *testing.T) {
		raw := []byte(`{"input_tokens":100,"output_tokens":50,"new_category":999}`)
		usage := parseTokenUsage(raw)
		if usage.InputTokens != 100 {
			t.Errorf("InputTokens = %d", usage.InputTokens)
		}
		if usage.Extra == nil || usage.Extra["new_category"] != 999 {
			t.Errorf("Extra = %v, want new_category=999", usage.Extra)
		}
	})

	t.Run("nil_input", func(t *testing.T) {
		usage := parseTokenUsage(nil)
		if usage.InputTokens != 0 {
			t.Errorf("InputTokens = %d, want 0", usage.InputTokens)
		}
	})
}
