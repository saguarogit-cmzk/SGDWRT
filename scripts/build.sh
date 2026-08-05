#!/bin/sh
# Cross-compile saguaro-core za uređaj (linux/amd64, statični binary).
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
cd core
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags "-s -w" -o ../dist/saguaro-core .
cd ..
# provjera sučelja (dvostruki id-evi, elementi kojih nema) — preskače se samo
# ako na stroju nema node-a, da build i dalje prolazi
if command -v node >/dev/null 2>&1; then
	node scripts/webcheck.js
else
	echo "upozorenje: nema node-a, provjera sučelja preskočena"
fi

rm -rf dist/web
cp -r web dist/web
ls -lh dist/saguaro-core
ls dist/web
