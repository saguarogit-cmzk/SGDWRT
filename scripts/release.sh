#!/bin/sh
# Složi release paket (lokalno): saguaro-<ver>-linux-amd64.tar.gz u dist/.
# Objava na GitHub ide automatski kroz .github/workflows/release.yml pri
# pushu taga (git tag vX.Y.Z && git push --tags); ova skripta služi za
# ručne pakete koje se učita kroz GUI (Ažuriranje -> Ručna nadogradnja).
set -e
cd "$(dirname "$0")/.."

VER=$(grep -o 'version = "[^"]*"' core/main.go | cut -d'"' -f2)
echo ">> gradim v$VER"
bash scripts/build.sh

STAGE=$(mktemp -d)
cp dist/saguaro-core "$STAGE/"
mkdir -p "$STAGE/web"
cp dist/web/* "$STAGE/web/"
cp image/files/etc/init.d/saguaro-core "$STAGE/init.d-saguaro-core"

OUT="dist/saguaro-v$VER-linux-amd64.tar.gz"
tar -czf "$OUT" -C "$STAGE" .
rm -rf "$STAGE"
echo ">> $OUT"
