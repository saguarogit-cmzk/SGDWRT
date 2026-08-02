#!/bin/sh
# Saguaro — samoprovjera uređaja.
#
# Pokreće se NA uređaju, kao root:
#   sh /opt/saguaro/selftest.sh
#
# Provjerava stvarno stanje (nftables, procesi, portovi, ubus), ne samo
# konfiguraciju — jer "postoji u configu" i "radi" nisu isto. Ništa ne mijenja
# na uređaju; jedina iznimka je test oporavka servisa, koji se pokreće samo uz
# zastavicu --disruptive i uredno vraća stanje.
#
# Izlazni kod: 0 = sve prošlo, 1 = bar jedan pad.

BASE=/opt/saguaro
API="https://127.0.0.1:8443/api/v1"
PASS=0; FAIL=0; SKIP=0
DISRUPTIVE=0
[ "$1" = "--disruptive" ] && DISRUPTIVE=1

G=""; R=""; Y=""; N=""
if [ -t 1 ]; then G="\033[32m"; R="\033[31m"; Y="\033[33m"; N="\033[0m"; fi

# funkcije uvijek vraćaju uspjeh: inače bi "bad" bez drugog argumenta vratio
# neuspjeh, pa bi se u lancu "provjera && bad || ok" ispisalo oboje
ok()   { PASS=$((PASS+1)); printf "  ${G}PROŠLO${N}  %s\n" "$1"; return 0; }
bad()  { FAIL=$((FAIL+1)); printf "  ${R}PALO${N}    %s\n" "$1"
         [ -n "$2" ] && printf "          %s\n" "$2"; return 0; }
skip() { SKIP=$((SKIP+1)); printf "  ${Y}PRESKAČEM${N} %s\n" "$1"
         [ -n "$2" ] && printf "          %s\n" "$2"; return 0; }
head_() { printf "\n== %s ==\n" "$1"; }

TOKEN=$(cat "$BASE/etc/token" 2>/dev/null)
api() { curl -sk -H "Authorization: Bearer $TOKEN" "$API/$1"; }
apicode() { curl -sk -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOKEN" "$API/$1"; }

printf "Saguaro samoprovjera — %s\n" "$(date '+%d.%m.%Y. %H:%M')"

# ---------------------------------------------------------------- 1. osnovno
head_ "Servis i sučelje"

if /etc/init.d/saguaro-core status 2>/dev/null | grep -q running; then
    ok "Saguaro servis radi"
else
    bad "Saguaro servis ne radi" "pokreni: /etc/init.d/saguaro-core start"
fi

if [ -f /etc/rc.d/S95saguaro-core ]; then
    ok "Saguaro se pokreće pri podizanju uređaja"
else
    bad "Saguaro NIJE uključen u pokretanje" "/etc/init.d/saguaro-core enable"
fi

if [ "$(apicode health)" = "200" ]; then
    ok "API odgovara"
else
    bad "API ne odgovara na $API/health"
fi

if [ -n "$TOKEN" ] && [ "$(apicode system)" = "200" ]; then
    ok "Prijava tokenom radi"
else
    bad "Prijava tokenom ne radi"
fi

if [ "$(curl -sk -o /dev/null -w '%{http_code}' "$API/system")" = "401" ]; then
    ok "Bez tokena API odbija pristup"
else
    bad "API odgovara i BEZ tokena" "ozbiljno: provjeri auth međusloj"
fi

# sigurnosna zaglavlja i TLS
H=$(curl -sk -D - -o /dev/null "$API/health")
for hdr in "x-frame-options" "content-security-policy" "x-content-type-options"; do
    if echo "$H" | grep -qi "^$hdr:"; then
        ok "Zaglavlje $hdr je prisutno"
    else
        bad "Nedostaje zaglavlje $hdr"
    fi
done
if curl -sk --tlsv1.1 --tls-max 1.1 "$API/health" >/dev/null 2>&1; then
    bad "Sučelje prihvaća zastarjeli TLS 1.1"
else
    ok "Zastarjeli TLS (1.1 i stariji) je odbijen"
fi

# ------------------------------------------------------------- 2. povezanost
head_ "Internet, DNS i vrijeme"

if ping -c2 -W3 1.1.1.1 >/dev/null 2>&1; then
    ok "Internet je dostupan (ping 1.1.1.1)"
else
    bad "Nema odgovora s interneta" "provjeri WAN vezu i zadanu rutu"
fi

if nslookup openwrt.org 127.0.0.1 2>/dev/null | grep -q "Address"; then
    ok "Pretvorba imena u adrese (DNS) radi"
else
    bad "DNS ne radi" "provjeri dnsmasq i upstream poslužitelje"
fi

# DNSSEC: krivotvoreni potpis mora biti odbijen
if [ "$(uci -q get dhcp.@dnsmasq[0].dnssec)" = "1" ]; then
    if nslookup dnssec-failed.org 127.0.0.1 2>&1 | grep -qiE "SERVFAIL|can't find|no answer"; then
        ok "DNSSEC odbija krivotvoreni odgovor"
    else
        bad "DNSSEC je uključen, ali propušta krivotvoreni odgovor"
    fi
else
    skip "DNSSEC nije uključen"
fi

DEFRT=$(ip route show default | head -1)
if [ -n "$DEFRT" ]; then
    ok "Zadana ruta postoji ($(echo "$DEFRT" | awk '{print $3, $5}'))"
else
    bad "Nema zadane rute prema internetu"
fi

# više zadanih ruta: ona s najmanjom metrikom mora biti WAN, ne LAN
NDEF=$(ip route show default | wc -l)
if [ "$NDEF" -gt 1 ]; then
    TOPDEV=$(ip route show default | head -1 | sed 's/.*dev \([^ ]*\).*/\1/')
    if [ "$TOPDEV" = "br-lan" ]; then
        bad "Promet uređaja izlazi kroz LAN, ne WAN" \
            "LAN ruta ima prednost — provjeri metriku (network.lan.metric)"
    else
        ok "Zadani izlaz ide kroz $TOPDEV (ne kroz LAN)"
    fi
fi

if [ "$(uci -q get system.@system[0].zonename)" != "" ]; then
    ok "Vremenska zona: $(uci -q get system.@system[0].zonename), sat: $(date '+%H:%M')"
else
    skip "Vremenska zona nije postavljena"
fi

# ------------------------------------------------------------- 3. firewall
head_ "Firewall"

if [ "$(uci -q get firewall.@defaults[0].input)" != "ACCEPT" ] &&
   [ "$(uci -q get firewall.@defaults[0].forward)" != "ACCEPT" ]; then
    ok "Zadane politike su restriktivne (input/forward nisu ACCEPT)"
else
    bad "Zadana politika firewalla propušta sve" "opasno u produkciji"
fi

if nft list ruleset >/dev/null 2>&1; then
    ok "nftables skup pravila je učitan ($(nft list ruleset 2>/dev/null | wc -l) redaka)"
else
    bad "nftables skup pravila nije dostupan"
fi

# konfiguracija vs stvarno stanje: zaostali lanci obrisanih zona
GEN=$(fw4 print 2>/dev/null | grep -c "chain input_")
LIVE=$(nft list table inet fw4 2>/dev/null | grep -c "chain input_")
if [ "$GEN" = "$LIVE" ]; then
    ok "Firewall u jezgri odgovara konfiguraciji ($GEN zona)"
else
    bad "Razlika: konfiguracija ima $GEN zona, jezgra $LIVE" \
        "zaostali lanci obrisanih zona — riješi s: /etc/init.d/firewall restart"
fi

# upravljanje ne smije biti otvoreno prema internetu
OPEN=""
for p in 22 80 443 8443; do
    nft list chain inet fw4 input_wan 2>/dev/null | grep -q "dport $p .*accept" && OPEN="$OPEN $p"
done
if [ -z "$OPEN" ]; then
    ok "Upravljanje (SSH, LuCI, Saguaro) nije otvoreno prema internetu"
else
    bad "S interneta su dostupni portovi:$OPEN" "ukloni ta pravila osim ako su namjerna"
fi

# ---------------------------------------------------------------- 4. VPN
head_ "VPN"

if [ "$(uci -q get network.sag_wg0.proto)" = "wireguard" ]; then
    if wg show sag_wg0 >/dev/null 2>&1; then
        NP=$(wg show sag_wg0 peers 2>/dev/null | grep -c .)
        ok "WireGuard radi (korisnika: $NP)"
        HS=$(wg show sag_wg0 latest-handshakes 2>/dev/null | awk '$2>0' | wc -l)
        if [ "$NP" -gt 0 ] && [ "$HS" -gt 0 ]; then
            ok "Bar jedan WireGuard korisnik se stvarno spojio"
        elif [ "$NP" -gt 0 ]; then
            skip "Nijedan WireGuard korisnik se još nije spojio" \
                 "spoji stvarnog klijenta pa ponovi — ovo je jedini pravi dokaz"
        fi
    else
        bad "WireGuard je konfiguriran, ali sučelje ne radi" "ifup sag_wg0"
    fi
else
    skip "WireGuard nije postavljen"
fi

if [ "$(uci -q get openvpn.sag_server.enabled)" = "1" ]; then
    if pgrep openvpn >/dev/null; then
        ok "OpenVPN poslužitelj radi"
        OVPID=$(pgrep openvpn | head -1)
        if grep -q "^Uid:" "/proc/$OVPID/status" 2>/dev/null; then
            UID_=$(awk '/^Uid:/{print $2}' "/proc/$OVPID/status")
            if [ "$UID_" != "0" ]; then
                ok "OpenVPN je odustao od root ovlasti (uid $UID_)"
            else
                bad "OpenVPN radi kao root" "postavi user nobody / group nogroup"
            fi
        fi
        CONF=/var/etc/openvpn-sag_server.conf
        # drugi faktor uz certifikat: korisničko ime i lozinka
        if grep -q "^auth-user-pass-verify" "$CONF" 2>/dev/null; then
            # grep -c ispiše 0 ali vrati neuspjeh, pa se zadano postavlja zasebno
            NPW=$(grep -c . "$BASE/etc/ovpn/users" 2>/dev/null)
            [ -n "$NPW" ] || NPW=0
            if [ "$NPW" -gt 0 ]; then
                ok "OpenVPN traži i lozinku uz certifikat (korisnika s lozinkom: $NPW)"
            else
                bad "OpenVPN traži lozinku, ali je nitko nema" \
                    "nijedan se korisnik ne može prijaviti dok mu se ne postavi"
            fi
        else
            skip "OpenVPN traži samo certifikat" \
                 "tko dobije .ovpn datoteku, spojio se — razmisli o lozinci"
        fi
        for opt in "crl-verify" "data-ciphers" "tls-version-min"; do
            if grep -q "^$opt" "$CONF" 2>/dev/null; then
                ok "OpenVPN: $opt je postavljen"
            else
                bad "OpenVPN: nedostaje $opt"
            fi
        done
        # redak koji POČINJE s CLIENT_LIST je stvaran klijent; redak koji
        # počinje s HEADER samo opisuje stupce
        if grep -q "^CLIENT_LIST," /tmp/sag_ovpn.status 2>/dev/null; then
            ok "Bar jedan OpenVPN klijent je trenutno spojen"
        else
            skip "Nijedan OpenVPN klijent se nije spojio" \
                 "spoji stvarnog klijenta pa ponovi — ovo je jedini pravi dokaz"
        fi
    else
        bad "OpenVPN je uključen, ali proces ne radi"
    fi
else
    skip "OpenVPN nije postavljen"
fi

# VPN korisnici ne smiju do upravljanja uređajem
for z in sagwg sagovpn; do
    if nft list chain inet fw4 "input_$z" >/dev/null 2>&1; then
        if nft list chain inet fw4 "input_$z" | grep -qE "dport (22|443|8443).*accept"; then
            bad "VPN zona $z ima pristup upravljanju uređajem" \
                "namjerno? inače isključi kvačicu u VPN modulu"
        else
            ok "VPN zona $z nema pristup upravljanju uređajem"
        fi
    fi
done

# ------------------------------------------------------------- 5. zaštita
head_ "Zaštita"

if [ -f /etc/init.d/banip ] && [ "$(uci -q get banip.global.ban_enabled)" = "1" ]; then
    # statusi ovih paketa idu na stderr, pa se mora hvatati i on
    BSTAT=$(/etc/init.d/banip status 2>&1)
    DEV=$(echo "$BSTAT" | grep "active_devices" | sed 's/.*wan: \([^ /]*\).*/\1/')
    # uzima se dio iza PRVE dvotočke; iza broja slijedi još "(chains: ...)"
    CNT=$(echo "$BSTAT" | grep "element_count" | sed 's/^[^:]*: *//; s/ *(.*//')
    if [ "$DEV" = "br-lan" ]; then
        bad "banIP filtrira LAN sučelje umjesto interneta" \
            "posljedica pogrešne zadane rute — vidi test zadane rute gore"
    elif [ -n "$DEV" ]; then
        ok "banIP štiti internet sučelje ($DEV, zapisa: $CNT)"
    else
        bad "banIP ne prijavljuje nijedno zaštićeno sučelje"
    fi
    if echo "$BSTAT" | grep -q "nft: ✔"; then
        ok "banIP pravila su stvarno u jezgri"
    else
        bad "banIP nema pravila u jezgri"
    fi
else
    skip "banIP nije uključen"
fi

if [ -f /etc/init.d/adblock-fast ]; then
    AD=$(/etc/init.d/adblock-fast status 2>&1 | head -1)
    if echo "$AD" | grep -q "blocking"; then
        ok "Blokada domena radi ($(echo "$AD" | grep -oE '[0-9]+ domains'))"
    else
        skip "Blokada domena nije aktivna"
    fi
fi

# detekcija skeniranja portova
if [ -f /etc/nftables.d/20-saguaro-scan.nft ]; then
    if nft list chain inet fw4 sag_scan_detect >/dev/null 2>&1; then
        NSC=$(nft list set inet fw4 sag_scanners 2>/dev/null |
              grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | grep -c .)
        ok "Detekcija skeniranja portova radi (trenutno blokiranih: $NSC)"
    else
        bad "Pravila za detekciju skeniranja postoje, ali nisu u jezgri" \
            "/etc/init.d/firewall restart"
    fi
else
    skip "Detekcija skeniranja portova nije uključena"
fi

# trag promjena konfiguracije
if api audit | grep -q '"changes"'; then
    ok "Trag promjena konfiguracije je aktivan"
else
    bad "Trag promjena konfiguracije ne odgovara"
fi

# očvršćivanje
if [ "$(sysctl -n net.ipv4.conf.all.rp_filter 2>/dev/null)" = "2" ]; then
    ok "Obrana od krivotvorenih adresa je uključena (labavo)"
else
    skip "Obrana od krivotvorenih adresa nije uključena" "Postavke → Očvršćivanje"
fi

# adresa WAN sučelja se čita sa sučelja, ne iz uci — kod DHCP-a uci je prazan
WANIP=""
for wi in $(uci -q get firewall.@zone[1].network) wan; do
    wd=$(uci -q get "network.$wi.device")
    [ -n "$wd" ] || continue
    a=$(ip -4 addr show "$wd" 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)
    [ -n "$a" ] && WANIP="$a" && break
done
if [ -z "$WANIP" ]; then
    skip "Ne mogu očitati adresu internet sučelja" "provjera DNS-a na WAN-u preskočena"
elif netstat -tuln 2>/dev/null | grep -q "$WANIP:53"; then
    bad "DNS sluša na internet sučelju ($WANIP)" \
        "Postavke → Očvršćivanje → DNS ne osluškuje na internet sučelju"
else
    ok "DNS ne sluša na internet sučelju ($WANIP)"
fi

# ------------------------------------------------------- 6. logovi i backup
head_ "Logovi, backup i upozorenja"

if [ "$(uci -q get system.@system[0].log_file)" = "$BASE/log/system.log" ]; then
    if [ -f "$BASE/log/system.log" ]; then
        ok "Logovi se trajno spremaju na disk ($(ls "$BASE/log" | wc -l) datoteka)"
    else
        bad "Trajno spremanje je uključeno, ali datoteka ne postoji"
    fi
    grep -q "sag-logrotate" /etc/crontabs/root 2>/dev/null &&
        ok "Noćna rotacija logova je zakazana" ||
        bad "Rotacija logova nije zakazana" "logovi bi rasli bez kraja"
else
    skip "Logovi se ne spremaju trajno" "nestaju pri svakom restartu — Postavke"
fi

NB=$(ls "$BASE/backup"/full-*.tar.gz 2>/dev/null | wc -l)
if [ "$NB" -gt 0 ]; then
    LAST=$(ls -t "$BASE/backup"/full-*.tar.gz | head -1)
    if tar tzf "$LAST" >/dev/null 2>&1; then
        ok "Zadnja arhiva je ispravna ($(basename "$LAST"))"
    else
        bad "Zadnja arhiva je oštećena" "$LAST"
    fi
else
    bad "Nema nijednog punog backupa" "Backup → Izradi backup"
fi

if grep -q "sag-backup" /etc/crontabs/root 2>/dev/null; then
    ok "Automatski backup je zakazan"
else
    skip "Automatski backup nije zakazan"
fi

# sysupgrade zadano ne cuva /etc/sysctl.d ni /opt — bez ovog popisa bi se
# ocvrscivanje, token, certifikati i baza izgubili pri nadogradnji firmwarea
if [ -f /lib/upgrade/keep.d/saguaro ]; then
    MISS=""
    for f in $(grep -v '^#' /lib/upgrade/keep.d/saguaro); do
        sysupgrade -l 2>/dev/null | grep -q "^$f" || MISS="$MISS $f"
    done
    if [ -z "$MISS" ]; then
        ok "Saguaro postavke preživljavaju nadogradnju firmwarea"
    else
        bad "Nadogradnja firmwarea bi izgubila:$MISS"
    fi
else
    bad "Popis za nadogradnju firmwarea ne postoji" \
        "/lib/upgrade/keep.d/saguaro — postavke bi se izgubile pri sysupgradeu"
fi

OFF=$(api backup/offsite)
if echo "$OFF" | grep -q '"enabled":true'; then
    if echo "$OFF" | grep -q '"last_ok":""'; then
        bad "Slanje backupa izvan uređaja je uključeno, ali nikad nije uspjelo" \
            "Backup → Pošalji zadnju arhivu odmah, pa pogledaj grešku"
    else
        ok "Backup se šalje izvan uređaja"
    fi
else
    skip "Backup se ne šalje izvan uređaja" \
         "arhive postoje samo na ovom disku — kvar diska znači gubitak svega"
fi

# u odgovoru /monitor i pojedini nadzirani uređaji imaju polje enabled,
# pa se traži baš ono unutar objekta email
if api monitor | grep -q '"email":{"enabled":true'; then
    ok "Slanje e-mail obavijesti je uključeno"
    skip "Stvarno slanje e-maila nije provjereno" \
         "Nadzor → Pošalji probnu poruku (jedini pravi dokaz)"
else
    skip "E-mail obavijesti nisu uključene" "uređaj neće javiti kad nešto padne"
fi

# ------------------------------------------------ 7. oporavak (disruptivno)
head_ "Oporavak servisa"

if [ "$DISRUPTIVE" = "1" ]; then
    if pgrep openvpn >/dev/null; then
        killall openvpn 2>/dev/null
        sleep 8
        if pgrep openvpn >/dev/null; then
            ok "OpenVPN se sam vratio nakon prekida"
        else
            bad "OpenVPN se NIJE vratio nakon prekida" "provjeri procd respawn"
            /etc/init.d/openvpn start >/dev/null 2>&1
        fi
    else
        skip "OpenVPN ne radi — nema što provjeriti"
    fi
else
    skip "Test oporavka servisa" "pokreni sa: sh selftest.sh --disruptive"
fi

# ---------------------------------------------------------------- sažetak
printf "\n===================================================\n"
printf " Prošlo: %s   Palo: %s   Preskočeno: %s\n" "$PASS" "$FAIL" "$SKIP"
printf "===================================================\n"
if [ "$FAIL" -gt 0 ]; then
    printf "\nIma padova — pogledaj retke označene s PALO.\n"
    exit 1
fi
printf "\nSve provjere su prošle.\n"
printf "Preskočene stavke nisu greške, nego ono što treba provjeriti ručno\n"
printf "ili nije uključeno.\n"
exit 0
