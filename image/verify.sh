#!/bin/sh
# Saguaro — provjera izgrađene slike prije nego ode korisniku.
#
# Slika se ne isporučuje na vjeru: ovdje se otvara i gleda što je stvarno
# unutra. Pokreće se iz gradnje (GitHub Actions) ili ručno na Linuxu:
#   sudo sh image/verify.sh dist/saguaro-....img.gz
#
# Provjerava:
#   1. tablicu particija u slici — root mora biti tražene veličine
#   2. sadržaj root particije — Saguaro program, sučelje i init skripte
#   3. skriptu prvog dizanja

set -eu

IMG="${1:?Upotreba: verify.sh <slika.img.gz>}"
WANT_ROOT_MB="${WANT_ROOT_MB:-1024}"
TMP=$(mktemp -d)
MNT="$TMP/mnt"
RAW="$TMP/disk.img"

cleanup() {
    mountpoint -q "$MNT" 2>/dev/null && umount "$MNT" || true
    [ -n "${LOOP:-}" ] && losetup -d "$LOOP" 2>/dev/null || true
    rm -rf "$TMP"
}
trap cleanup EXIT

fail() { echo "!! $1"; exit 1; }
ok()   { echo "   OK  $1"; }

echo ">> provjera slike: $(basename "$IMG")"
mkdir -p "$MNT"
gunzip -c "$IMG" > "$RAW"

# ---------------------------------------------------- 1. tablica particija
echo ">> tablica particija"
ROOT_MB=0
i=0
while [ $i -lt 4 ]; do
    OFF=$((446 + i * 16))
    TYPE=$(dd if="$RAW" bs=1 skip=$((OFF + 4)) count=1 2>/dev/null | od -An -tu1 | tr -d ' ')
    if [ "$TYPE" != "0" ]; then
        SIZE=$(dd if="$RAW" bs=1 skip=$((OFF + 12)) count=4 2>/dev/null |
               od -An -tu4 | tr -d ' ')
        MB=$((SIZE / 2048))
        echo "   particija $((i + 1)): tip 0x$(printf '%02x' "$TYPE"), ${MB} MB"
        # NE koristiti "[ uvjet ] && var=..." — uz set -e neuspio uvjet ruši
        # skriptu na prvoj particiji koja nije root
        if [ $i -eq 1 ]; then
            ROOT_MB=$MB
        fi
    fi
    i=$((i + 1))
done
[ "$ROOT_MB" -gt 0 ] || fail "u slici nema druge particije (root)"
# ImageBuilder zaokružuje, pa se dopušta mala razlika
DIFF=$((ROOT_MB - WANT_ROOT_MB))
[ $DIFF -lt 0 ] && DIFF=$((-DIFF))
[ $DIFF -le 16 ] || fail "root particija je ${ROOT_MB} MB, a traženo je ${WANT_ROOT_MB} MB"
ok "root particija ${ROOT_MB} MB"

# ------------------------------------------------------- 2. sadržaj roota
echo ">> sadržaj root particije"
LOOP=$(losetup -f --show -P "$RAW")
echo "   loop uređaj: $LOOP"
n=0
while [ ! -b "${LOOP}p2" ] && [ $n -lt 20 ]; do
    sleep 1
    n=$((n + 1))
done
[ -b "${LOOP}p2" ] || fail "jezgra ne vidi drugu particiju slike (${LOOP}p2)"
mount -o ro "${LOOP}p2" "$MNT" || fail "root particija se ne montira"

for f in \
    /opt/saguaro/bin/saguaro-core \
    /opt/saguaro/web/index.html \
    /opt/saguaro/web/app.js \
    /opt/saguaro/web/style.css \
    /opt/saguaro/selftest.sh \
    /etc/init.d/saguaro-core \
    /etc/init.d/saguaro-datapart \
    /etc/uci-defaults/99-saguaro-firstboot
do
    [ -e "$MNT$f" ] || fail "u slici nedostaje $f"
    ok "$f"
done

[ -x "$MNT/opt/saguaro/bin/saguaro-core" ] || fail "saguaro-core nije izvršan"
ok "saguaro-core je izvršan ($(du -h "$MNT/opt/saguaro/bin/saguaro-core" | cut -f1))"

# paketi koji su nužni za data particiju i rad sustava
for p in parted mkfs.ext4 wg openvpn; do
    find "$MNT" -name "$p" -type f 2>/dev/null | grep -q . ||
        fail "u slici nema alata $p"
    ok "alat $p"
done

echo
echo ">> slika je u redu"
