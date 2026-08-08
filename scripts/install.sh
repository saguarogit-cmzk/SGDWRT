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

# --- provjere platforme prije ijedne izmjene --------------------------------
# apk postoji tek od OpenWrt 25.x; na starijem (opkg) skripta ne radi
if ! command -v apk >/dev/null 2>&1; then
    echo "!! ovaj uređaj nema 'apk' — Saguaro traži OpenWrt 25.x ili noviji."
    echo "   (na 23.05/24.10 s opkg instalacija nije podržana)"
    exit 1
fi
# arhitektura: izdanje je x86-64; na ARM/MIPS uređaju binary se ne pokreće
ARCH=$(uname -m 2>/dev/null || echo "?")
case "$ARCH" in
    x86_64|amd64) ;;
    *)
        echo "!! arhitektura uređaja je '$ARCH', a izdanje je za x86-64."
        echo "   Saguaro se zasad isporučuje samo za x86-64 (IN100 i slični)."
        exit 1
        ;;
esac

echo ">> paketi (WireGuard, OpenVPN, mwan3, banIP, adblock-fast, dnsmasq-full...)"
apk update >/dev/null
# dnsmasq-full mijenja osnovni dnsmasq (potreban za DNSSEC). VAŽNO: prvo se
# dodaje zamjena, tek onda miče stari — ako 'add' padne (nema interneta ili
# mjesta), uređaj ostaje s ispravnim dnsmasqom umjesto bez DNS-a i DHCP-a.
# apk sam istisne osnovni dnsmasq jer je dnsmasq-full u sukobu s njim.
# parted/partx/e2fsprogs/block-mount trebaju za data particiju (datapart.go);
# qrencode za QR kod dvofaktorske prijave.
apk add dnsmasq-full wireguard-tools kmod-wireguard openvpn-openssl mwan3 \
    banip adblock-fast grep sed coreutils-sort curl \
    bird2 bird2c sqm-scripts ddns-scripts ddns-scripts-services nlbwmon \
    ppp kmod-pppoe \
    parted partx-utils e2fsprogs block-mount qrencode
# nlbwmon (mjerenje prometa po uređaju) radi u pozadini
/etc/init.d/nlbwmon enable >/dev/null 2>&1 || true
/etc/init.d/nlbwmon start >/dev/null 2>&1 || true
/etc/init.d/dnsmasq restart >/dev/null 2>&1 || true

echo ">> direktoriji"
mkdir -p "$BASE/bin" "$BASE/web" "$BASE/etc" "$BASE/data" "$BASE/backup" "$BASE/log"

TAR="$1"
if [ -z "$TAR" ]; then
    echo ">> preuzimam zadnje izdanje s GitHuba"
    # -f: 404 postaje greška umjesto da se HTML spremi kao "paket"
    META=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest") || {
        echo "!! ne mogu dohvatiti popis izdanja s GitHuba (internet? repo?)"; exit 1; }
    URL=$(echo "$META" | grep browser_download_url | grep 'linux-amd64.tar.gz"' | head -1 | cut -d'"' -f4)
    SHAURL=$(echo "$META" | grep browser_download_url | grep 'linux-amd64.tar.gz.sha256' | head -1 | cut -d'"' -f4)
    if [ -z "$URL" ]; then
        echo "!! nema objavljenih izdanja — pokreni: sh install.sh <paket.tar.gz>"
        exit 1
    fi
    curl -fsSL -o /tmp/saguaro-install.tar.gz "$URL" || {
        echo "!! preuzimanje paketa nije uspjelo"; exit 1; }
    TAR=/tmp/saguaro-install.tar.gz
    # provjera otiska ako izdanje ima .sha256 (novija izdanja ga imaju)
    if [ -n "$SHAURL" ]; then
        WANT=$(curl -fsSL "$SHAURL" | awk '{print $1}')
        GOT=$(sha256sum "$TAR" | awk '{print $1}')
        if [ -n "$WANT" ] && [ "$WANT" != "$GOT" ]; then
            echo "!! otisak paketa se ne slaže — prekidam (očekivano $WANT, dobiveno $GOT)"
            exit 1
        fi
        echo ">> otisak paketa provjeren"
    fi
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

# Konzolni alati (ako ih paket sadrži): čarobnjak i reset lozinke, podsjetnik
# na konzoli, te init za vraćanje data particije nakon nadogradnje. Bez ovoga
# je install.sh put bio osiromašen u odnosu na gotovu sliku.
if [ -f "$TMP/saguaro-setup" ]; then
    cp "$TMP/saguaro-setup" /usr/sbin/saguaro-setup
    chmod 755 /usr/sbin/saguaro-setup
fi
if [ -f "$TMP/profile.d-99-saguaro.sh" ]; then
    cp "$TMP/profile.d-99-saguaro.sh" /etc/profile.d/99-saguaro.sh
    chmod 644 /etc/profile.d/99-saguaro.sh
fi
if [ -f "$TMP/init.d-saguaro-datapart" ]; then
    cp "$TMP/init.d-saguaro-datapart" /etc/init.d/saguaro-datapart
    chmod 755 /etc/init.d/saguaro-datapart
    /etc/init.d/saguaro-datapart enable >/dev/null 2>&1 || true
fi
rm -rf "$TMP"

# Zadanu lozinku prve prijave (Sgs#2026) postavlja sam servis pri prvom
# startu; API token servis generira nasumičan. Ovdje se NIŠTA ne upisuje —
# stariji install.sh je lozinku upisivao u datoteku tokena, a Bearer token
# zaobilazi branu obavezne promjene lozinke, pa je javno poznati token bio
# otvorena vrata na LAN-u.

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
echo " Sučelje pri prvoj prijavi samo traži novu lozinku."
echo " API token je nasumičan (System -> Settings ako ga trebaš)."
echo ""
echo " Konzola: 'saguaro-setup' (adresa, reset lozinke sučelja)."
echo " Napomena: ovaj put NE stvara zasebnu data particiju (rizično na"
echo " uređaju koji već radi) — podaci su na root particiji. Za punu"
echo " podjelu (root 1 GB + data particija) koristi gotovu sliku."
echo "=================================================================="
