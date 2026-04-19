#!/usr/bin/env bash
# Configure git to use the repo's .githooks directory for hooks.
# This works across worktrees because each checkout contains the
# tracked .githooks/ directory and they share .git/config.
set -euo pipefail
git config core.hooksPath .githooks
chmod +x .githooks/*
printf "Git hooks path set to .githooks\n"
