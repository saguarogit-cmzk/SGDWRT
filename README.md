# Saguaro Infrastructure

Upravljačka platforma za IN100 appliance uređaje na bazi OpenWrt-a.
OpenWrt je "motor ispod haube" — Saguaro je korisničko iskustvo.

## Model

**Standalone (Opcija A):** Saguaro živi na svakom uređaju. Inventory je
lokalna SQLite baza. Shema je od prvog dana "sync-ready" (UUID po uređaju),
tako da kasniji prelazak na centralni kontroler ostaje moguć bez migracije.

## Ciljna platforma

- IN100 appliance: x86_64, 4× 2.5GbE, 8 GB RAM, 200 GB disk
- OpenWrt **25.12.x stable** — paketni manager je **apk**, firewall je fw4/nftables
- Saguaro se instalira u `/opt/saguaro` (zasebna particija — preživljava sysupgrade)

## Struktura repozitorija

```
core/       Saguaro Core — čita stanje sustava (ubus/uci), servira REST API
api/        API specifikacija (OpenAPI) i dokumentacija endpointa
web/        Frontend (SPA) — Dashboard, kasnije Devices/DHCP/DNS/VPN...
modules/    Moduli: inventory, dhcp, dns, wireguard, backup
scripts/    Deploy, dijagnostika, provisioning
image/      ImageBuilder konfiguracija za "Golden Base" (ponovljiv image)
docs/       Arhitektura, mrežni plan, odluke (ADR)
```

## Pravila razvoja

1. Razvoj se radi na radnoj stanici, na uređaj ide **deploy** (`scripts/deploy.sh`).
   Nikad se ne razvija direktno na routeru.
2. Core **ne parsira izlaze naredbi** — isključivo `ubus` (JSON) i `uci`.
3. Saguaro **ne zamjenjuje** OpenWrt servise (dnsmasq, wireguard, fw4) —
   njima **upravlja** kroz UCI.
4. LuCI ostaje netaknut kao servisni ulaz dok Saguaro web ne pokrije funkciju.
