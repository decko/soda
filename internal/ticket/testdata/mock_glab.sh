#!/usr/bin/env bash
# Mock glab CLI for testing. Returns fixture JSON from environment variables.
#
# When any argument contains "notes", serves MOCK_GLAB_NOTES_FIXTURE.
# Otherwise serves MOCK_GLAB_FIXTURE.
#
# Usage:
#   MOCK_GLAB_FIXTURE=gitlab_fetch.json ./mock_glab.sh issue view 42 --repo group/project --output json
#   MOCK_GLAB_NOTES_FIXTURE=gitlab_notes.json ./mock_glab.sh api projects/group%2Fproject/issues/42/notes
set -euo pipefail

FIXTURE_DIR="$(dirname "$0")"

# Check if any argument contains "notes" — if so, serve the notes fixture.
for arg in "$@"; do
    if [[ "$arg" == *"notes"* ]]; then
        fixture_file="${FIXTURE_DIR}/${MOCK_GLAB_NOTES_FIXTURE:-}"
        if [[ ! -f "$fixture_file" ]]; then
            echo "mock_glab: notes fixture not found: $fixture_file" >&2
            exit 1
        fi
        cat "$fixture_file"
        exit 0
    fi
done

fixture_file="${FIXTURE_DIR}/${MOCK_GLAB_FIXTURE:-}"

if [[ ! -f "$fixture_file" ]]; then
    echo "mock_glab: fixture not found: $fixture_file" >&2
    exit 1
fi

cat "$fixture_file"
