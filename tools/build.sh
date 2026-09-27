#!/usr/bin/env bash
# Build, vet and unit-test dvbhub. Output also goes to build/build.log.
cd "$(dirname "$0")/.."
mkdir -p build
{
  rm -rf internal/dvr
  echo "== gofmt"; gofmt -l .
  echo "== vet"; go vet ./... 2>&1
  echo "== build"; go build -o build/dvbhub ./cmd/dvbhub 2>&1 && echo "build ok"
  echo "== test"; go test ./... 2>&1
  echo "== done"
} 2>&1 | tee build/build.log
