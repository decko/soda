#!/usr/bin/env bash
# Mock gh CLI for testing. Returns fixture JSON from MOCK_GH_FIXTURE env var.
#
# Usage:
#   MOCK_GH_FIXTURE=github_fetch.json ./mock_gh.sh issue view 36 --repo owner/repo --json ...
set -euo pipefail

FIXTURE_DIR="$(dirname "$0")"
fixture_file="${FIXTURE_DIR}/${MOCK_GH_FIXTURE:-}"

if [[ ! -f "$fixture_file" ]]; then
    echo "mock_gh: fixture not found: $fixture_file" >&2
    exit 1
fi

cat "$fixture_file"
