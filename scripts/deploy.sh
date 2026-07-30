#!/bin/sh
# Deploy Saguaro na uređaj. Razvoj je na radnoj stanici, uređaj samo prima build.
# Upotreba:  sh scripts/deploy.sh root@192.168.100.1
# Preduvjet: build artefakti u ./dist/ (npr. dist/saguaro-core, dist/web/)

set -eu
TARGET="${1:?Upotreba: deploy.sh root@<ip>}"
DEST=/opt/saguaro

[ -d dist ] || { echo "Nema ./dist — prvo napravi build."; exit 1; }

ssh "$TARGET" "mkdir -p $DEST/bin $DEST/web $DEST/etc $DEST/data $DEST/log"

# scp -O = legacy SCP protokol — dropbear na OpenWrt-u nema sftp-server
[ -f dist/saguaro-core ] && scp -O dist/saguaro-core "$TARGET:$DEST/bin/saguaro-core.new"
[ -d dist/web ] && scp -O -r dist/web/. "$TARGET:$DEST/web/"
[ -f image/files/etc/init.d/saguaro-core ] && scp -O image/files/etc/init.d/saguaro-core "$TARGET:/etc/init.d/saguaro-core"

ssh "$TARGET" "
  chmod +x /etc/init.d/saguaro-core 2>/dev/null || true
  if [ -f $DEST/bin/saguaro-core.new ]; then
    chmod +x $DEST/bin/saguaro-core.new
    mv $DEST/bin/saguaro-core.new $DEST/bin/saguaro-core
    /etc/init.d/saguaro-core enable
    /etc/init.d/saguaro-core restart
  fi
"
echo "Deploy na $TARGET gotov."
