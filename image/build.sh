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
mkdir -p "$FILES/opt/saguaro/bin" "$FILES/opt/saguaro/web"

# cijelo stablo image/files ide u sliku kakvo jest — tako se nova datoteka
# (npr. /etc/board.d/…) ne mora dodavati i ovdje da bi završila u slici
cp -a "$ROOT/image/files/." "$FILES/"

cp "$ROOT/dist/saguaro-core"   "$FILES/opt/saguaro/bin/"
cp "$ROOT"/web/*               "$FILES/opt/saguaro/web/"
cp "$ROOT/scripts/selftest.sh" "$FILES/opt/saguaro/selftest.sh"

chmod +x "$FILES/opt/saguaro/bin/saguaro-core" "$FILES/opt/saguaro/selftest.sh"
for d in init.d uci-defaults board.d; do
    [ -d "$FILES/etc/$d" ] && chmod +x "$FILES/etc/$d/"* || true
done

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

# ---------------------------------------------- jedinstven potpis diska
#
# OpenWrt svakoj slici daje isti potpis diska u MBR-u, pa svi uređaji
# instalirani iz njih imaju i isti PARTUUID. Jezgra root traži upravo preko
# PARTUUID-a — a ako su u istom računalu dva takva diska (npr. USB stick za
# instalaciju i već instalirani interni disk), opis odgovara objema
# particijama i uzme se prva. Posljedica: digneš se sa sticka, a sustav ti
# krene s diska. Provjereno na uređaju 05.08.2026.
#
# Zato svaka slika dobiva vlastiti potpis. Mijenjaju se 4 bajta u MBR-u i
# jednako dugački zapis PARTUUID-a u grub.cfg — sve na razini bajtova, bez
# montiranja, pa gradnja i dalje ne traži root ovlasti.
RAW="$WORK/personalize.img"
gunzip -c "$SRC" > "$RAW"

OLD_SIG=$(dd if="$RAW" bs=1 skip=440 count=4 2>/dev/null | od -An -tx1 | tr -d ' \n')
# PARTUUID je isti zapis, samo obrnutim redoslijedom bajtova
OLD_UUID=$(echo "$OLD_SIG" | sed 's/\(..\)\(..\)\(..\)\(..\)/\4\3\2\1/')
NEW_SIG=$(head -c4 /dev/urandom | od -An -tx1 | tr -d ' \n')
NEW_UUID=$(echo "$NEW_SIG" | sed 's/\(..\)\(..\)\(..\)\(..\)/\4\3\2\1/')
echo ">> potpis diska: $OLD_SIG -> $NEW_SIG (PARTUUID $OLD_UUID -> $NEW_UUID)"

# Bajtovi se pišu preko oktalnog zapisa: "\xNN" ne podržava svaki printf
# (provjereno — negdje ispadne 12 bajtova umjesto 4), a "\NNN" podržava svaki.
hex2bin() {
    h="$1"
    while [ -n "$h" ]; do
        b=$(printf '%.2s' "$h"); h=$(printf '%s' "$h" | cut -c3-)
        printf "\\$(printf '%03o' "$((16#$b))")"
    done
}
hex2bin "$NEW_SIG" > "$WORK/sig.bin"
[ "$(wc -c < "$WORK/sig.bin")" -eq 4 ] || {
    echo "!! potpis nije 4 bajta nego $(wc -c < "$WORK/sig.bin")"; exit 1; }
dd if="$WORK/sig.bin" of="$RAW" bs=1 seek=440 count=4 conv=notrunc 2>/dev/null

# svaki zapis starog PARTUUID-a (u grub.cfg) zamijeni novim; jednake su
# duljine, pa se datoteka ne pomiče i ništa se drugo ne mijenja
HITS=0
for OFF in $(grep -abo -F "$OLD_UUID-0" "$RAW" | cut -d: -f1); do
    printf '%s' "$NEW_UUID" | dd of="$RAW" bs=1 seek="$OFF" count=8 conv=notrunc 2>/dev/null
    HITS=$((HITS + 1))
done
echo ">> PARTUUID zamijenjen na $HITS mjesta"
[ "$HITS" -gt 0 ] || { echo "!! stari PARTUUID nije nađen u slici — grub bi ostao neusklađen"; exit 1; }

gzip -c "$RAW" > "$DST"
rm -f "$RAW"
# otisak se zapisuje uz samo ime datoteke, ne uz punu putanju s build stroja —
# inače `sha256sum -c` kod korisnika ne radi
( cd "$OUT" && sha256sum "$(basename "$DST")" > "$(basename "$DST").sha256" )

echo
echo ">> gotovo: $DST"
echo ">> otisak: $(cut -d' ' -f1 < "$DST.sha256")"
echo ">> veličina: $(du -h "$DST" | cut -f1)"
echo
echo "Napiši je na USB (Rufus, način DD image), digni uređaj s USB-a ili je"
echo "upiši na disk uređaja, pa otvori https://192.168.1.1:8443/"
