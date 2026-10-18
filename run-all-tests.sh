#!/usr/bin/env bash

set -euo pipefail

go_files=()
while IFS= read -r file; do
  go_files+=("$file")
done < <(git ls-files --cached --others --exclude-standard -- '*.go')

echo "Checking gofmt ..."
unformatted="$(gofmt -l "${go_files[@]}")"
if [[ -n "$unformatted" ]]; then
  echo "Unformatted Go files:"
  echo "$unformatted"
  exit 1
fi

echo "Running Go tests ..."
go test ./...

echo "Running go vet ..."
go vet ./...

echo "Running golangci-lint ..."
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...

echo "Running Go race tests ..."
go test -race ./...

# An ignore-start without a matching ignore-end silently suppresses duplication
# detection to the end of that file and still exits successfully, so unbalanced
# markers must fail before the scan reports a clean result.
echo "Checking duplication marker balance ..."
unbalanced=0
for file in "${go_files[@]}"; do
  starts="$(grep -c 'jscpd:ignore-start' "$file" || true)"
  ends="$(grep -c 'jscpd:ignore-end' "$file" || true)"
  if [[ "$starts" -ne "$ends" ]]; then
    echo "$file has $starts jscpd:ignore-start and $ends jscpd:ignore-end markers"
    unbalanced=1
  fi
done
if [[ "$unbalanced" -ne 0 ]]; then
  echo "Unbalanced duplication markers hide later clones in the same file."
  exit 1
fi

python3 -m unittest discover -s packaging/cloudsmith -v
bash -n packaging/cloudsmith/smoke-test.sh

echo "Running code duplication search ..."
# Shared with CI: only generated output goldens are excluded, not templates
# or the independently maintained testdata/report.body.html fixture.
npx --yes jscpd@5.1.2 --config .jscpd.json --no-tips .

if command -v govulncheck >/dev/null 2>&1; then
  echo "Running Go vulnerability scan ..."
  govulncheck ./...
fi

if command -v hugo >/dev/null 2>&1 && command -v mandoc >/dev/null 2>&1; then
  echo "Building documentation site ..."
  # Same build as .github/workflows/docs.yml; fails on any warning or broken link.
  make docs
else
  echo "Skipping documentation site build (needs hugo and mandoc) ..."
fi

echo "Running Trivy vulnerability scan ..."
trivy fs . --skip-dirs .agents --ignorefile .trivyignore.yaml

echo "🥳🥳 All tests passed. 🎉🎉 "
