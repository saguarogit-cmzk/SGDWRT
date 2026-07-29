#!/bin/sh
# Prikupi činjenice s uređaja preko SSH-a i spremi ih lokalno.
# Upotreba:  sh scripts/device-facts.sh root@192.168.100.1
# Izlaz:     docs/facts/<datum>/*.json|txt  — služi kao snimka stanja uređaja.

set -eu
TARGET="${1:?Upotreba: device-facts.sh root@<ip>}"
OUT="docs/facts/$(date +%F)"
mkdir -p "$OUT"

run() { # run <ime-datoteke> <udaljena naredba...>
  f="$1"; shift
  echo "== $f"
  ssh "$TARGET" "$@" > "$OUT/$f" 2>&1 || echo "(greška: $*)" >> "$OUT/$f"
}

run system-board.json   ubus call system board
run system-info.json    ubus call system info
run network-dump.json   ubus call network.interface dump
run links.json          ip -j link
run addrs.json          ip -j addr
run routes.txt          ip route
run disk.txt            "df -h; echo ---; cat /proc/partitions; echo ---; block info"
run memory.txt          "free; echo ---; cat /proc/cpuinfo | grep -E 'model name|processor' | sort -u"
run packages.txt        "apk list --installed 2>/dev/null || opkg list-installed"
run uci-network.txt     uci export network
run uci-firewall.txt    uci export firewall
run uci-dhcp.txt        uci export dhcp
run uci-system.txt      uci export system

echo
echo "Gotovo — snimka u $OUT/"
