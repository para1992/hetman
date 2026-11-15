#!/usr/bin/env bash
# Builds the release binaries for every supported target and a SHA-256
# checksum file. It uses only the vendored dependencies, so it needs no
# network.
#
#   scripts/release.sh [VERSION] [OUT_DIR]
#
# VERSION defaults to GITHUB_REF_NAME (set for a tag build on GitHub) and then
# to the tag that points at the current commit. OUT_DIR defaults to dist.
# Files written:
#   hetman-VERSION-OS-ARCH[.exe]                  the engine, one binary per target
#   hetman-adapter-claude-VERSION-OS-ARCH[.exe]   the adapter for the Claude Code
#                                                 client, one binary per target
#   hetman-adapter-opencode-VERSION-OS-ARCH[.exe] the adapter for the OpenCode
#                                                 client, one binary per target
#   hetman-adapter-codex-VERSION-OS-ARCH[.exe]    the adapter for the Codex
#                                                 client, one binary per target
#   hetman-VERSION-SHA256SUMS                     checksums of all of them in
#                                                 sha256sum format
set -euo pipefail
cd "$(dirname "$0")/.."

version=${1:-${GITHUB_REF_NAME:-}}
if [ -z "$version" ]; then
  version=$(git describe --tags --exact-match 2>/dev/null || true)
fi
if [ -z "$version" ]; then
  echo "usage: $0 VERSION [OUT_DIR] (or run on a commit with a tag)" >&2
  exit 2
fi
if ! [[ $version =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]]; then
  echo "invalid version \"$version\": use letters, digits, '.', '_', '+' and '-'" >&2
  exit 2
fi
out=${2:-dist}

export GOFLAGS=-mod=vendor
export GOPROXY=off
export GOTOOLCHAIN=local
export CGO_ENABLED=0

if command -v sha256sum >/dev/null; then
  sha() { sha256sum "$@"; }
else
  sha() { shasum -a 256 "$@"; }
fi

mkdir -p "$out"
rm -f "$out/hetman-$version-"* "$out/hetman-adapter-claude-$version-"* "$out/hetman-adapter-opencode-$version-"* \
  "$out/hetman-adapter-codex-$version-"*

files=()
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  ext=""
  if [ "$os" = windows ]; then ext=".exe"; fi
  name="hetman-$version-$os-$arch$ext"
  # -X sets the version string for a binary that declares it; the linker
  # ignores it otherwise.
  GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -buildid= -X hetman/internal/cli.Version=$version" \
    -o "$out/$name" ./cmd/hetman
  files+=("$name")
  echo "built $name"
  name="hetman-adapter-claude-$version-$os-$arch$ext"
  GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -buildid=" \
    -o "$out/$name" ./cmd/hetman-adapter-claude
  files+=("$name")
  echo "built $name"
  name="hetman-adapter-opencode-$version-$os-$arch$ext"
  GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -buildid=" \
    -o "$out/$name" ./cmd/hetman-adapter-opencode
  files+=("$name")
  echo "built $name"
  name="hetman-adapter-codex-$version-$os-$arch$ext"
  GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -buildid=" \
    -o "$out/$name" ./cmd/hetman-adapter-codex
  files+=("$name")
  echo "built $name"
done

sums="hetman-$version-SHA256SUMS"
(cd "$out" && sha "${files[@]}" > "$sums")
echo "wrote $sums"
