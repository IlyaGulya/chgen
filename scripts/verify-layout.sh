#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

output="$(go test ./internal/repositorycheck -run '^TestStandardGoLayoutContract$' -count=1 -v)"
echo "${output}"
if ! grep -Fq -- '--- PASS: TestStandardGoLayoutContract' <<<"${output}"; then
	echo "The standard Go layout contract did not run." >&2
	exit 1
fi
