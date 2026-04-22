package main

import (
	"context"
	"strings"
	"testing"
)

func TestRenderSpecPrompt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	prompt, err := renderSpecPrompt(ctx, dir, "add a health check endpoint")
	if err != nil {
		t.Fatalf("renderSpecPrompt failed: %v", err)
	}

	if !strings.Contains(prompt, "add a health check endpoint") {
		t.Error("prompt should contain the description")
	}

	if !strings.Contains(prompt, "token budget") {
		t.Error("prompt should mention token budget estimation")
	}

	if !strings.Contains(prompt, "ticket_body") {
		t.Error("prompt should instruct about ticket_body output field")
	}
}

func TestRenderSpecPrompt_WithDetection(t *testing.T) {
	ctx := context.Background()
	prompt, err := renderSpecPrompt(ctx, ".", "test description")
	if err != nil {
		t.Fatalf("renderSpecPrompt failed: %v", err)
	}

	if prompt == "" {
		t.Error("prompt should not be empty")
	}
}

func TestNewSpecCmd_Flags(t *testing.T) {
	cmd := newSpecCmd()

	if cmd.Use != "spec <description>" {
		t.Errorf("unexpected Use: %s", cmd.Use)
	}

	flags := []string{"from-file", "yes", "dry-run"}
	for _, flag := range flags {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing flag: %s", flag)
		}
	}
}
