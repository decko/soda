package claude

import (
	"os"
	"testing"
)

func TestExtractDisplayText(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "text_block",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll read the file now."}]}}`,
			want: "I'll read the file now.",
		},
		{
			name: "tool_use_block",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"main.go"}}]}}`,
			want: "[Read]",
		},
		{
			name: "text_and_tool_use",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Reading file."},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]}}`,
			want: "Reading file. [Bash]",
		},
		{
			name: "system_event",
			line: `{"type":"system","subtype":"init","session_id":"abc"}`,
			want: "",
		},
		{
			name: "user_event",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok"}]}}`,
			want: "",
		},
		{
			name: "result_event",
			line: `{"type":"result","subtype":"success","result":"Done."}`,
			want: "",
		},
		{
			name: "empty_text",
			line: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"  "}]}}`,
			want: "",
		},
		{
			name: "invalid_json",
			line: `not json at all`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDisplayText([]byte(tt.line))
			if got != tt.want {
				t.Errorf("extractDisplayText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShouldPersist(t *testing.T) {
	assistantText := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Thinking..."}]}}`
	assistantTool := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"main.go"}}]}}`
	userToolResult := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"data"}]}}`
	systemEvent := `{"type":"system","subtype":"init","session_id":"abc"}`
	resultEvent := `{"type":"result","subtype":"success","result":"Done."}`

	tests := []struct {
		name  string
		line  string
		level TranscriptLevel
		want  bool
	}{
		// TranscriptOff
		{"off_assistant_text", assistantText, TranscriptOff, false},
		{"off_assistant_tool", assistantTool, TranscriptOff, false},

		// TranscriptTools
		{"tools_assistant_text", assistantText, TranscriptTools, false},
		{"tools_assistant_tool", assistantTool, TranscriptTools, true},
		{"tools_user_tool_result", userToolResult, TranscriptTools, true},
		{"tools_system", systemEvent, TranscriptTools, false},
		{"tools_result", resultEvent, TranscriptTools, false},

		// TranscriptFull
		{"full_assistant_text", assistantText, TranscriptFull, true},
		{"full_assistant_tool", assistantTool, TranscriptFull, true},
		{"full_user_tool_result", userToolResult, TranscriptFull, true},
		{"full_system", systemEvent, TranscriptFull, false},
		{"full_result", resultEvent, TranscriptFull, false},

		// Invalid JSON
		{"invalid_json", "not json", TranscriptFull, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldPersist([]byte(tt.line), tt.level)
			if got != tt.want {
				t.Errorf("shouldPersist(%s) = %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}

func TestParseTranscriptEntry(t *testing.T) {
	t.Run("text_block", func(t *testing.T) {
		line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]}}`
		entries := parseTranscriptEntry([]byte(line))
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].Role != "assistant" {
			t.Errorf("Role = %q, want %q", entries[0].Role, "assistant")
		}
		if entries[0].Content != "Hello world" {
			t.Errorf("Content = %q, want %q", entries[0].Content, "Hello world")
		}
	})

	t.Run("tool_use_block", func(t *testing.T) {
		line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"main.go"}}]}}`
		entries := parseTranscriptEntry([]byte(line))
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].Role != "tool_use" {
			t.Errorf("Role = %q, want %q", entries[0].Role, "tool_use")
		}
		if entries[0].Tool != "Read" {
			t.Errorf("Tool = %q, want %q", entries[0].Tool, "Read")
		}
		if entries[0].ToolID != "tu_1" {
			t.Errorf("ToolID = %q, want %q", entries[0].ToolID, "tu_1")
		}
	})

	t.Run("tool_result_block", func(t *testing.T) {
		line := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"file content here"}]}}`
		entries := parseTranscriptEntry([]byte(line))
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].Role != "tool_result" {
			t.Errorf("Role = %q, want %q", entries[0].Role, "tool_result")
		}
		if entries[0].Content != "file content here" {
			t.Errorf("Content = %q, want %q", entries[0].Content, "file content here")
		}
		if entries[0].ToolID != "tu_1" {
			t.Errorf("ToolID = %q, want %q", entries[0].ToolID, "tu_1")
		}
	})

	t.Run("multiple_blocks", func(t *testing.T) {
		line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Reading file."},{"type":"tool_use","id":"tu_2","name":"Bash","input":{"command":"ls"}}]}}`
		entries := parseTranscriptEntry([]byte(line))
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}
		if entries[0].Role != "assistant" {
			t.Errorf("entries[0].Role = %q, want %q", entries[0].Role, "assistant")
		}
		if entries[1].Role != "tool_use" {
			t.Errorf("entries[1].Role = %q, want %q", entries[1].Role, "tool_use")
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		entries := parseTranscriptEntry([]byte("not json"))
		if len(entries) != 0 {
			t.Errorf("expected 0 entries for invalid JSON, got %d", len(entries))
		}
	})
}

func TestExtractToolResultContent(t *testing.T) {
	t.Run("string_content", func(t *testing.T) {
		result := extractToolResultContent([]byte(`"hello world"`))
		if result != "hello world" {
			t.Errorf("got %q, want %q", result, "hello world")
		}
	})

	t.Run("array_content", func(t *testing.T) {
		result := extractToolResultContent([]byte(`[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]`))
		if result != "line1\nline2" {
			t.Errorf("got %q, want %q", result, "line1\nline2")
		}
	})

	t.Run("empty_content", func(t *testing.T) {
		result := extractToolResultContent(nil)
		if result != "" {
			t.Errorf("got %q, want empty", result)
		}
	})
}

func TestFilterTranscript(t *testing.T) {
	data, err := os.ReadFile("testdata/stream_json_success.jsonl")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	t.Run("off_returns_nil", func(t *testing.T) {
		entries := FilterTranscript(data, TranscriptOff)
		if entries != nil {
			t.Errorf("expected nil, got %d entries", len(entries))
		}
	})

	t.Run("tools_level", func(t *testing.T) {
		entries := FilterTranscript(data, TranscriptTools)
		if len(entries) == 0 {
			t.Fatal("expected non-empty transcript at tools level")
		}
		// Should contain tool_use and tool_result entries but not plain text.
		for _, entry := range entries {
			if entry.Role == "assistant" {
				t.Errorf("tools level should not contain plain assistant text, got: %+v", entry)
			}
		}
		// Should have at least the Read and Bash tool uses and their results
		toolNames := make(map[string]bool)
		for _, entry := range entries {
			if entry.Tool != "" {
				toolNames[entry.Tool] = true
			}
		}
		if !toolNames["Read"] {
			t.Error("expected Read tool use in transcript")
		}
		if !toolNames["Bash"] {
			t.Error("expected Bash tool use in transcript")
		}
	})

	t.Run("full_level", func(t *testing.T) {
		entries := FilterTranscript(data, TranscriptFull)
		if len(entries) == 0 {
			t.Fatal("expected non-empty transcript at full level")
		}
		// Full level should include assistant text entries too.
		hasAssistantText := false
		for _, entry := range entries {
			if entry.Role == "assistant" {
				hasAssistantText = true
				break
			}
		}
		if !hasAssistantText {
			t.Error("full level should contain assistant text entries")
		}
		// Full should have more entries than tools.
		toolEntries := FilterTranscript(data, TranscriptTools)
		if len(entries) <= len(toolEntries) {
			t.Errorf("full (%d entries) should have more than tools (%d entries)", len(entries), len(toolEntries))
		}
	})

	t.Run("empty_input", func(t *testing.T) {
		entries := FilterTranscript([]byte{}, TranscriptFull)
		if len(entries) != 0 {
			t.Errorf("expected 0 entries for empty input, got %d", len(entries))
		}
	})
}

func TestTranscriptLevel_Values(t *testing.T) {
	// Verify the string constants match expected values.
	if TranscriptOff != "off" {
		t.Errorf("TranscriptOff = %q, want %q", TranscriptOff, "off")
	}
	if TranscriptTools != "tools" {
		t.Errorf("TranscriptTools = %q, want %q", TranscriptTools, "tools")
	}
	if TranscriptFull != "full" {
		t.Errorf("TranscriptFull = %q, want %q", TranscriptFull, "full")
	}
}
