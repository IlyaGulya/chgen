#!/usr/bin/env bash

# Keep the built command's exit status: go run would collapse refusal (2)
# and a measured failure (1) into the same exit code.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"
check_dir="$(mktemp -d -t chgen-check.XXXXXX)"
trap 'rmdir "$check_dir" 2>/dev/null || true' EXIT
go build -o "${check_dir}/verify" ./internal/tooling/cmd/verify
set +e
"${check_dir}/verify" "$@"
status=$?
set -e
# Only the binary just built by this wrapper is removed.
rm "${check_dir}/verify"
exit "$status"
