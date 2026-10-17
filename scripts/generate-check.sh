#!/bin/sh
# Regenerates all code and fails if it differs from what is committed.
set -eu

make generate >/dev/null
if [ -n "$(git status --porcelain -- gen internal/storage/sqlcgen)" ]; then
  echo "generate-check: generated code is out of date; run make generate and commit" >&2
  git status --short -- gen internal/storage/sqlcgen >&2
  exit 1
fi
echo "generate-check: generated code is up to date"
