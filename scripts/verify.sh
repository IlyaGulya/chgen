#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

expected_go="$(awk '/^toolchain go/ {print substr($2, 3)}' go.mod)"
actual_go="$(go env GOVERSION | sed 's/^go//')"
if [[ "${actual_go}" != "${expected_go}" ]]; then
	echo "go.mod needs Go ${expected_go}, but this command uses Go ${actual_go}." >&2
	exit 1
fi

unformatted="$(gofmt -l .)"
if [[ -n "${unformatted}" ]]; then
	echo "These files need gofmt:" >&2
	echo "${unformatted}" >&2
	exit 1
fi

go mod tidy -diff

"${repo_root}/scripts/verify-layout.sh"
go run ./internal/tooling/cmd/gensemantics -check
go run ./internal/tooling/cmd/typespecimens -check

# ST1005 covers established user-visible error text. Change that text only as
# a separate API change. U1000 cannot join uses across the mutually exclusive
# build-tag source sets. The checks below keep all correctness checks enabled.
staticcheck_checks="all,-ST1005,-U1000"
tag_sets=("" "fuzzoracle" "execoracle" "axisaudit")
for tag_set in "${tag_sets[@]}"; do
	if [[ -z "${tag_set}" ]]; then
		echo "Verify the default source set."
		go vet ./...
		go tool staticcheck -checks="${staticcheck_checks}" ./...
		continue
	fi

	echo "Verify the ${tag_set} source set."
	go vet -tags "${tag_set}" ./...
	go tool staticcheck -checks="${staticcheck_checks}" -tags "${tag_set}" ./...
	go test -tags "${tag_set}" -run '^$' ./...
done

# Run the complete offline type-oracle package. The build-only check above
# proves that each tagged source set compiles, but it cannot run the sampling
# plan, registry contract, or mutation gates.
go test -tags fuzzoracle ./internal/engine

go test ./...
go test ./examples -run '^TestGeneratedExamplesAreUpToDate$' -count=1
go test -race -shuffle=on ./...
"${repo_root}/scripts/external-consumer-smoke.sh"
