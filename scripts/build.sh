#!/bin/sh
# Cross-compile saguaro-core za uređaj (linux/amd64, statični binary).
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
cd core
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags "-s -w" -o ../dist/saguaro-core .
ls -lh ../dist/saguaro-core
