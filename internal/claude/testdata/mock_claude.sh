#!/bin/bash
# Mock Claude CLI for testing. Behavior controlled by MOCK_CLAUDE_MODE env var.

# Handle --version
for arg in "$@"; do
    if [ "$arg" = "--version" ]; then
        echo "claude-code 1.0.0-test"
        exit 0
    fi
done

case "${MOCK_CLAUDE_MODE}" in
    success)
        echo '{"type":"system","subtype":"init","session_id":"test123","tools":["Read","Bash"],"mcp_servers":[]}'
        echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Thinking..."}]}}'
        echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Reading files..."}]}}'
        echo '{"type":"result","subtype":"success","result":"Done.","structured_output":{"key":"value"},"total_cost_usd":0.05,"usage":{"input_tokens":100,"output_tokens":50},"num_turns":3,"duration_ms":5000}'
        ;;
    semantic_error)
        echo '{"type":"system","subtype":"init","session_id":"test123","tools":[],"mcp_servers":[]}'
        echo '{"type":"result","subtype":"error","result":"Something went wrong."}'
        ;;
    crash_rate_limit)
        echo "rate limit exceeded" >&2
        exit 1
        ;;
    crash_unknown)
        echo "something went wrong unexpectedly" >&2
        exit 1
        ;;
    slow)
        sleep 60
        ;;
    echo_stdin)
        # Read stdin and include it in the result for verification.
        # NOTE: Only works for simple strings without JSON special chars (" \ etc.)
        STDIN_CONTENT=$(cat)
        echo '{"type":"system","subtype":"init","session_id":"test123","tools":[],"mcp_servers":[]}'
        printf '{"type":"result","subtype":"success","result":"%s","structured_output":{}}\n' "$STDIN_CONTENT"
        ;;
    signal_kill)
        kill -KILL $$
        ;;
    signal_term)
        kill -TERM $$
        ;;
    tool_events)
        echo '{"type":"system","subtype":"init","session_id":"test123","tools":["Read","Bash"],"mcp_servers":[]}'
        echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"main.go"}}]}}'
        echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"file content"}]}}'
        echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_2","name":"Bash","input":{"command":"ls"}}]}}'
        echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_2","content":"main.go"}]}}'
        echo '{"type":"result","subtype":"success","result":"Done.","structured_output":{},"total_cost_usd":0.02,"usage":{"input_tokens":50,"output_tokens":20},"num_turns":2,"duration_ms":1000}'
        ;;
    *)
        echo "unknown mode: ${MOCK_CLAUDE_MODE}" >&2
        exit 1
        ;;
esac
