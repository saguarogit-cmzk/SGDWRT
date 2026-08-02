#!/bin/sh
# Saguaro Infrastructure — instalacija na svjež OpenWrt 25.x (apk) uređaj.
# Upotreba (na uređaju, kao root):
#   wget -O - https://raw.githubusercontent.com/saguarogit-cmzk/SGSWRT/main/scripts/install.sh | sh
# ili s lokalnim paketom:
#   sh install.sh saguaro-vX.Y.Z-linux-amd64.tar.gz
set -e

REPO="saguarogit-cmzk/SGSWRT"
BASE="/opt/saguaro"

echo ">> Saguaro Infrastructure — instalacija"

echo ">> paketi (WireGuard, OpenVPN, mwan3, banIP, adblock-fast, dnsmasq-full...)"
apk update >/dev/null
# dnsmasq-full mijenja osnovni dnsmasq (potreban za DNSSEC)
apk del dnsmasq >/dev/null 2>&1 || true
apk add dnsmasq-full wireguard-tools kmod-wireguard openvpn-openssl mwan3 \
    banip adblock-fast grep sed coreutils-sort curl \
    bird2 bird2c sqm-scripts ddns-scripts ddns-scripts-services nlbwmon \
    ppp kmod-pppoe
# nlbwmon (mjerenje prometa po uređaju) radi u pozadini
/etc/init.d/nlbwmon enable >/dev/null 2>&1 || true
/etc/init.d/nlbwmon start >/dev/null 2>&1 || true
/etc/init.d/dnsmasq restart >/dev/null 2>&1 || true

echo ">> direktoriji"
mkdir -p "$BASE/bin" "$BASE/web" "$BASE/etc" "$BASE/data" "$BASE/backup" "$BASE/log"

TAR="$1"
if [ -z "$TAR" ]; then
    echo ">> preuzimam zadnje izdanje s GitHuba"
    URL=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" \
        | grep browser_download_url | grep linux-amd64 | head -1 | cut -d'"' -f4)
    if [ -z "$URL" ]; then
        echo "!! nema objavljenih izdanja — pokreni: sh install.sh <paket.tar.gz>"
        exit 1
    fi
    curl -sL -o /tmp/saguaro-install.tar.gz "$URL"
    TAR=/tmp/saguaro-install.tar.gz
fi

echo ">> raspakiravam $TAR"
TMP=$(mktemp -d)
tar -xzf "$TAR" -C "$TMP"
[ -f "$TMP/saguaro-core" ] || { echo "!! paket ne sadrži saguaro-core"; exit 1; }

/etc/init.d/saguaro-core stop >/dev/null 2>&1 || true
cp "$TMP/saguaro-core" "$BASE/bin/saguaro-core"
chmod 755 "$BASE/bin/saguaro-core"
[ -d "$TMP/web" ] && cp "$TMP"/web/* "$BASE/web/"
# samoprovjera uređaja: sh /opt/saguaro/selftest.sh
if [ -f "$TMP/selftest.sh" ]; then
    cp "$TMP/selftest.sh" "$BASE/selftest.sh"
    chmod 755 "$BASE/selftest.sh"
fi

if [ -f "$TMP/init.d-saguaro-core" ]; then
    cp "$TMP/init.d-saguaro-core" /etc/init.d/saguaro-core
else
    cat > /etc/init.d/saguaro-core <<'EOF'
#!/bin/sh /etc/rc.common
START=95
USE_PROCD=1
start_service() {
    procd_open_instance
    procd_set_param command /opt/saguaro/bin/saguaro-core
    procd_set_param respawn
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
EOF
fi
chmod 755 /etc/init.d/saguaro-core
rm -rf "$TMP"

# zadana lozinka prve prijave (admin račun nastaje iz tokena pri prvom startu)
if [ ! -s "$BASE/etc/token" ]; then
    printf 'Sgs#2026\n' > "$BASE/etc/token"
    chmod 600 "$BASE/etc/token"
fi

echo ">> pokrećem servis"
/etc/init.d/saguaro-core enable
/etc/init.d/saguaro-core start
sleep 2

IP=$(uci -q get network.lan.ipaddr || echo "<adresa-uređaja>")
echo ""
echo "=================================================================="
echo " Saguaro je instaliran i radi:  https://$IP:8443/"
echo " Prva prijava:  korisnik 'admin'   lozinka 'Sgs#2026'"
echo ""
echo " VAŽNO: odmah promijeni lozinku (Sustav -> Postavke) i"
echo " regeneriraj API token (ista stranica) — zadane vrijednosti"
echo " su iste na svakoj novoj instalaciji."
echo "=================================================================="
