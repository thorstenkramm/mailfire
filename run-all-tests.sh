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
golangci-lint run ./...

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

echo "Running code duplication search ..."
# These four files are byte-for-byte rendered-output goldens maintained by
# testutil.Golden (-update), not independently maintained implementations.
# Keep templates/base.html and testdata/report.body.html in the scan.
npx --yes jscpd@5.1.2 --min-lines 10 --min-tokens 50 --threshold 0 \
  --reporters console --no-tips \
  --ignore "**/*_test.go,**/vendor/**,**/_bmad/**,**/.cache/**,**/_bmad-output/**,**/.agents/**,**/internal/cli/testdata/note_inlined.html,**/internal/cli/testdata/preheader_footer_inlined.html,**/internal/render/testdata/note.html,**/internal/render/testdata/report.html" \
  --format go,markup .

if command -v govulncheck >/dev/null 2>&1; then
  echo "Running Go vulnerability scan ..."
  govulncheck ./...
fi

echo "Running Trivy vulnerability scan ..."
trivy fs . --skip-dirs .agents --ignorefile .trivyignore.yaml

echo "🥳🥳 All tests passed. 🎉🎉 "
