#!/usr/bin/env bash
# Quick developer check: format, vet, build, unit tests. Output also in build/check.log.
cd "$(dirname "$0")/.."
mkdir -p build
{
  gofmt -l -w internal cmd tools
  echo "== vet";   go vet ./... 2>&1
  echo "== build"; go build -o build/dvbhub ./cmd/dvbhub 2>&1 && echo "build ok"
  echo "== test";  go test ./... 2>&1 | grep -v 'no test files'
  echo "== detect"; ./build/dvbhub -detect -data build/detect-data 2>&1 | head -40
  echo "== bash -n"; for f in deploy/*.sh tools/*.sh; do bash -n "$f" || echo "syntax error in $f"; done; echo "scripts ok"
  echo "== done"
} 2>&1 | tee build/check.log
