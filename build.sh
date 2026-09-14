#!/bin/sh
# Cross-compile static binaries for every supported platform into dist/.
set -e
cd "$(dirname "$0")"
VERSION=$(grep -o 'version = "[^"]*"' main.go | cut -d'"' -f2)
rm -rf dist && mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}; arch=${target#*/}
  out="dist/cuh-$os-$arch"; [ "$os" = windows ] && out="$out.exe"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "$out" .
  echo "built $out"
done
echo "version $VERSION"
