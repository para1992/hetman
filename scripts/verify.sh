#!/usr/bin/env bash
# Runs every check a continuous-integration job needs: formatting, vet,
# build, tests and a cross-build of all release targets. It uses only the
# vendored dependencies, so it needs no network.
set -euo pipefail
cd "$(dirname "$0")/.."

export GOFLAGS=-mod=vendor
export GOPROXY=off
export CGO_ENABLED=0

echo "== gofmt"
unformatted=$(gofmt -l cmd internal)
if [ -n "$unformatted" ]; then
  echo "not formatted:"
  echo "$unformatted"
  exit 1
fi

echo "== client names"
# The core never names a client: adapters do. Client and vendor names may
# appear only in cmd/hetman-adapter-*, the examples of those adapters and the
# README section on adapters.
names='claude|anthropic|codex|openai|gemini'
if found=$(grep -rniE "$names" internal cmd/hetman); then
  echo "client or vendor names in the core ($names):"
  echo "$found"
  exit 1
fi

echo "== go vet"
for os in linux darwin windows; do
  GOOS=$os go vet ./...
done

echo "== go build"
go build ./...

echo "== go test"
go test -count=1 ./...

echo "== translations"
# The catalogue test runs with the others above; here it reports, per
# language, the keys that are not translated yet.
go test -count=1 -run '^TestCatalogues$' -v ./internal/i18n | grep 'translation ' | sed 's/^ *[a-z0-9_]*\.go:[0-9]*: //'

echo "== cross-build"
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  ext=""
  if [ "$os" = windows ]; then ext=".exe"; fi
  GOOS=$os GOARCH=$arch go build -trimpath -o "$out/hetman-$os-$arch$ext" ./cmd/hetman
  GOOS=$os GOARCH=$arch go build -trimpath -o "$out/hetman-adapter-claude-$os-$arch$ext" ./cmd/hetman-adapter-claude
  echo "ok $target"
done

echo "== all checks passed"
