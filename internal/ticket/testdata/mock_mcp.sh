#!/usr/bin/env bash
# Mock MCP server for testing. Responds to JSON-RPC initialize and tools/call
# requests via stdin/stdout. Reads fixture responses from files specified
# by the MOCK_MCP_FIXTURE env var (one fixture per tools/call, comma-separated).
#
# Usage:
#   MOCK_MCP_FIXTURE=fetch.json ./mock_mcp.sh serve
#
# Protocol: newline-delimited JSON-RPC 2.0 over stdio.

set -euo pipefail

# Only handle "serve" subcommand (like real wtmcp)
if [[ "${1:-}" != "serve" ]]; then
    echo "mock_mcp: unknown command '${1:-}'" >&2
    exit 1
fi

FIXTURE_DIR="$(dirname "$0")"
IFS=',' read -ra FIXTURES <<< "${MOCK_MCP_FIXTURE:-}"
fixture_idx=0

while IFS= read -r line; do
    # Parse the method from the JSON-RPC request
    method=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin).get('method',''))" 2>/dev/null || true)
    id=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',0))" 2>/dev/null || true)

    case "$method" in
        initialize)
            echo "{\"jsonrpc\":\"2.0\",\"id\":${id},\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{\"tools\":{}},\"serverInfo\":{\"name\":\"mock-mcp\",\"version\":\"0.0.1\"}}}"
            ;;
        notifications/initialized)
            # Notification — no response
            ;;
        tools/call)
            if [[ $fixture_idx -lt ${#FIXTURES[@]} ]]; then
                fixture_file="${FIXTURE_DIR}/${FIXTURES[$fixture_idx]}"
                fixture_idx=$((fixture_idx + 1))
            else
                echo "{\"jsonrpc\":\"2.0\",\"id\":${id},\"error\":{\"code\":-1,\"message\":\"no more fixtures\"}}"
                continue
            fi

            if [[ ! -f "$fixture_file" ]]; then
                echo "{\"jsonrpc\":\"2.0\",\"id\":${id},\"error\":{\"code\":-1,\"message\":\"fixture not found: $fixture_file\"}}" >&2
                exit 1
            fi

            # Read fixture content as the tool result text
            content=$(python3 -c "
import json, sys
with open('$fixture_file') as f:
    data = f.read()
result = {'content': [{'type': 'text', 'text': data}]}
print(json.dumps({'jsonrpc': '2.0', 'id': $id, 'result': result}, separators=(',', ':')))" 2>/dev/null)
            echo "$content"
            ;;
        *)
            echo "{\"jsonrpc\":\"2.0\",\"id\":${id},\"error\":{\"code\":-32601,\"message\":\"method not found: $method\"}}"
            ;;
    esac
done
