package main

import (
	"testing"

	"github.com/decko/soda/internal/ticket"
	"github.com/decko/soda/internal/tui"
)

func TestNewPickCmd_Exists(t *testing.T) {
	cmd := newPickCmd()
	if cmd == nil {
		t.Fatal("newPickCmd() returned nil")
	}
	if cmd.Use != "pick" {
		t.Errorf("expected Use='pick', got %q", cmd.Use)
	}
}

func TestNewPickCmd_Flags(t *testing.T) {
	cmd := newPickCmd()

	queryFlag := cmd.Flags().Lookup("query")
	if queryFlag == nil {
		t.Fatal("--query flag not found")
	}
	if queryFlag.DefValue != "" {
		t.Errorf("--query default = %q, want empty", queryFlag.DefValue)
	}

	pipelineFlag := cmd.Flags().Lookup("pipeline")
	if pipelineFlag == nil {
		t.Fatal("--pipeline flag not found")
	}

	modeFlag := cmd.Flags().Lookup("mode")
	if modeFlag == nil {
		t.Fatal("--mode flag not found")
	}

	mockFlag := cmd.Flags().Lookup("mock")
	if mockFlag == nil {
		t.Fatal("--mock flag not found")
	}
}

func TestTicketsToPickerInfo(t *testing.T) {
	tickets := []ticket.Ticket{
		{
			Key:      "42",
			Summary:  "Add validation",
			Type:     "Feature",
			Priority: "High",
			Status:   "Open",
			Labels:   []string{"enhancement"},
		},
		{
			Key:      "58",
			Summary:  "Fix login bug",
			Type:     "Bug",
			Priority: "Critical",
			Status:   "In Progress",
			Labels:   nil,
		},
	}

	infos := ticketsToPickerInfo(tickets)

	if len(infos) != 2 {
		t.Fatalf("expected 2 infos, got %d", len(infos))
	}

	if infos[0].Key != "42" {
		t.Errorf("infos[0].Key = %q, want %q", infos[0].Key, "42")
	}
	if infos[0].Summary != "Add validation" {
		t.Errorf("infos[0].Summary = %q, want %q", infos[0].Summary, "Add validation")
	}
	if infos[0].Type != "Feature" {
		t.Errorf("infos[0].Type = %q, want %q", infos[0].Type, "Feature")
	}
	if infos[0].Priority != "High" {
		t.Errorf("infos[0].Priority = %q, want %q", infos[0].Priority, "High")
	}
	if infos[0].Status != "Open" {
		t.Errorf("infos[0].Status = %q, want %q", infos[0].Status, "Open")
	}
	if len(infos[0].Labels) != 1 || infos[0].Labels[0] != "enhancement" {
		t.Errorf("infos[0].Labels = %v, want [enhancement]", infos[0].Labels)
	}

	if infos[1].Key != "58" {
		t.Errorf("infos[1].Key = %q, want %q", infos[1].Key, "58")
	}
	if infos[1].Priority != "Critical" {
		t.Errorf("infos[1].Priority = %q, want %q", infos[1].Priority, "Critical")
	}
	if infos[1].Labels != nil {
		t.Errorf("infos[1].Labels = %v, want nil", infos[1].Labels)
	}
}

func TestTicketsToPickerInfo_Empty(t *testing.T) {
	infos := ticketsToPickerInfo(nil)
	if len(infos) != 0 {
		t.Errorf("expected 0 infos for nil input, got %d", len(infos))
	}
}

func TestPickerResultToTicketKey(t *testing.T) {
	result := &tui.PickerResult{
		Ticket: tui.TicketInfo{
			Key:     "PROJ-99",
			Summary: "Test ticket",
		},
		Action: tui.PickerActionRun,
	}

	if result.Ticket.Key != "PROJ-99" {
		t.Errorf("expected key 'PROJ-99', got %q", result.Ticket.Key)
	}
}
