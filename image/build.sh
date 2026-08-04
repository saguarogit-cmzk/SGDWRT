#!/bin/sh
# Saguaro Infrastructure — gradnja gotove slike za novi uređaj.
#
# Rezultat je jedna .img.gz datoteka u kojoj je SVE: OpenWrt, svi paketi,
# Saguaro program i sučelje, init skripte i postavljanje data particije pri
# prvom dizanju. Postupak kod korisnika je onda: napiši na USB, digni uređaj,
# prijavi se na sučelje.
#
# Upotreba (Linux ili GitHub Actions):
#   sh image/build.sh [verzija-openwrta]
# Zadano je izdanje iz OPENWRT_VERSION niže.
#
# Preduvjeti: curl, tar, zstd, make, gawk, unzip, python3 (ImageBuilder), te
# već izgrađen dist/saguaro-core (scripts/build.sh).

set -eu

OPENWRT_VERSION="${1:-25.12.5}"
TARGET="x86"
SUBTARGET="64"
PROFILE="generic"
# Root particija: najveće što ima smisla. Ostatak diska ide u data particiju,
# koja se stvara pri prvom dizanju (vidi files/etc/uci-defaults/99-saguaro-firstboot).
ROOTFS_PARTSIZE="${ROOTFS_PARTSIZE:-1024}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$ROOT/dist/imagebuilder"
OUT="$ROOT/dist"

IB_NAME="openwrt-imagebuilder-${OPENWRT_VERSION}-${TARGET}-${SUBTARGET}.Linux-x86_64"
IB_TAR="${IB_NAME}.tar.zst"
BASE_URL="https://downloads.openwrt.org/releases/${OPENWRT_VERSION}/targets/${TARGET}/${SUBTARGET}"

echo ">> Saguaro slika — OpenWrt ${OPENWRT_VERSION}, root ${ROOTFS_PARTSIZE} MB"

[ -f "$ROOT/dist/saguaro-core" ] || {
    echo "!! nema dist/saguaro-core — prvo pokreni scripts/build.sh"
    exit 1
}

mkdir -p "$WORK" "$OUT"

# ---------------------------------------------------------------- ImageBuilder
if [ ! -d "$WORK/$IB_NAME" ]; then
    echo ">> preuzimam ImageBuilder"
    curl -fsSL -o "$WORK/$IB_TAR" "$BASE_URL/$IB_TAR"

    echo ">> provjeravam otisak"
    WANT=$(curl -fsSL "$BASE_URL/sha256sums" | grep " \*$IB_TAR\$" | cut -d' ' -f1)
    [ -n "$WANT" ] || { echo "!! nema otiska za $IB_TAR"; exit 1; }
    GOT=$(sha256sum "$WORK/$IB_TAR" | cut -d' ' -f1)
    [ "$WANT" = "$GOT" ] || {
        echo "!! otisak ne odgovara ($GOT umjesto $WANT)"
        exit 1
    }

    echo ">> raspakiravam"
    tar --zstd -xf "$WORK/$IB_TAR" -C "$WORK"
fi

# ------------------------------------------------------------------- overlay
# Sve što ide u sliku pod / — priprema se u dist/, da se image/files u repou
# ne puni izgrađenim datotekama.
FILES="$WORK/files"
rm -rf "$FILES"
mkdir -p "$FILES/opt/saguaro/bin" "$FILES/opt/saguaro/web" \
         "$FILES/etc/init.d" "$FILES/etc/uci-defaults"

cp "$ROOT/dist/saguaro-core"            "$FILES/opt/saguaro/bin/"
cp "$ROOT"/web/*                        "$FILES/opt/saguaro/web/"
cp "$ROOT/scripts/selftest.sh"          "$FILES/opt/saguaro/selftest.sh"
cp "$ROOT"/image/files/etc/init.d/*     "$FILES/etc/init.d/"
cp "$ROOT"/image/files/etc/uci-defaults/* "$FILES/etc/uci-defaults/" 2>/dev/null || true
chmod +x "$FILES/opt/saguaro/bin/saguaro-core" "$FILES/opt/saguaro/selftest.sh" \
         "$FILES/etc/init.d/"* "$FILES/etc/uci-defaults/"* 2>/dev/null || true

# ------------------------------------------------------------------- paketi
PKGS=$(grep -v '^#' "$ROOT/image/packages.txt" | tr '\n' ' ')

echo ">> gradim sliku (ovo traje nekoliko minuta)"
make -C "$WORK/$IB_NAME" image \
    PROFILE="$PROFILE" \
    PACKAGES="$PKGS" \
    FILES="$FILES" \
    ROOTFS_PARTSIZE="$ROOTFS_PARTSIZE" \
    EXTRA_IMAGE_NAME="saguaro"

# ------------------------------------------------------------------- rezultat
SRC=$(find "$WORK/$IB_NAME/bin/targets/$TARGET/$SUBTARGET" \
      -name "*saguaro*ext4-combined.img.gz" | head -1)
[ -n "$SRC" ] || {
    echo "!! ImageBuilder nije napravio ext4-combined sliku"
    find "$WORK/$IB_NAME/bin/targets/$TARGET/$SUBTARGET" -name "*.img.gz"
    exit 1
}
SAG_VER=$(sed -n 's/^const version = "\(.*\)"/\1/p' "$ROOT/core/main.go")
DST="$OUT/saguaro-v${SAG_VER}-openwrt-${OPENWRT_VERSION}-x86-64.img.gz"
cp "$SRC" "$DST"
sha256sum "$DST" > "$DST.sha256"

echo
echo ">> gotovo: $DST"
echo ">> otisak: $(cut -d' ' -f1 < "$DST.sha256")"
echo ">> veličina: $(du -h "$DST" | cut -f1)"
echo
echo "Napiši je na USB (Rufus, način DD image), digni uređaj s USB-a ili je"
echo "upiši na disk uređaja, pa otvori https://192.168.1.1:8443/"
