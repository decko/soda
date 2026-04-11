package ticket

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
)

// mcpClient is a thin MCP (Model Context Protocol) client that communicates
// with an MCP server subprocess over stdio using JSON-RPC 2.0.
type mcpClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
	stderr *bytes.Buffer
	nextID atomic.Int64
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// newMCPClient spawns an MCP server process and performs the protocol
// initialization handshake. The caller must call close() when done.
func newMCPClient(ctx context.Context, binary string, extraArgs ...string) (*mcpClient, error) {
	args := make([]string, 0, len(extraArgs)+1)
	args = append(args, extraArgs...)
	args = append(args, "serve")
	cmd := exec.CommandContext(ctx, binary, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderrBuf, max: 64 * 1024} // 64KB cap

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", binary, err)
	}

	client := &mcpClient{
		cmd:    cmd,
		stdin:  stdin,
		reader: bufio.NewReaderSize(stdout, 1024*1024), // 1MB line buffer
		stderr: &stderrBuf,
	}

	if err := client.initialize(); err != nil {
		client.close()
		return nil, err
	}

	return client, nil
}

func (c *mcpClient) initialize() error {
	initParams := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "soda",
			"version": "0.1.0",
		},
	}

	_, err := c.send("initialize", initParams)
	if err != nil {
		return fmt.Errorf("mcp: initialize: %w", err)
	}

	// Send initialized notification (no ID, no response expected)
	notification := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	data, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("mcp: marshal notification: %w", err)
	}
	if _, err := fmt.Fprintf(c.stdin, "%s\n", data); err != nil {
		return fmt.Errorf("mcp: send notification: %w", err)
	}

	return nil
}

func (c *mcpClient) send(method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal request: %w", err)
	}

	if _, err := fmt.Fprintf(c.stdin, "%s\n", data); err != nil {
		return nil, fmt.Errorf("mcp: write: %w", err)
	}

	// Read response lines until we find one with matching ID.
	// Skip notifications and log lines the server may emit.
	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("mcp: read response: %w", err)
		}

		var resp jsonRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // skip non-JSON lines
		}

		if resp.ID != id {
			continue // skip notifications or other responses
		}

		if resp.Error != nil {
			return nil, fmt.Errorf("mcp: server error %d: %s", resp.Error.Code, resp.Error.Message)
		}

		return resp.Result, nil
	}
}

// callTool invokes a named tool and returns the concatenated text content.
func (c *mcpClient) callTool(name string, arguments map[string]any) (string, error) {
	params := map[string]any{
		"name":      name,
		"arguments": arguments,
	}

	raw, err := c.send("tools/call", params)
	if err != nil {
		return "", err
	}

	var result mcpToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("mcp: unmarshal tool result: %w", err)
	}

	if result.IsError {
		var msgs []string
		for _, block := range result.Content {
			if block.Type == "text" {
				msgs = append(msgs, block.Text)
			}
		}
		return "", fmt.Errorf("mcp: tool error: %s", strings.Join(msgs, "; "))
	}

	var texts []string
	for _, block := range result.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}

	return strings.Join(texts, ""), nil
}

func (c *mcpClient) close() error {
	_ = c.stdin.Close() // best-effort; Wait() is the authoritative error
	return c.cmd.Wait()
}

// stderrText returns captured server stderr for diagnostics.
func (c *mcpClient) stderrText() string {
	return c.stderr.String()
}

// limitedWriter writes up to max bytes, silently discarding excess.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.max - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		w.buf.Write(p[:remaining])
		return len(p), nil
	}
	return w.buf.Write(p)
}
