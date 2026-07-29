# Arhitektura — Saguaro (standalone model)

```
┌─────────────────────────────────────────────────┐
│                 Saguaro Web (SPA)               │  :8443 (HTTPS)
│   Dashboard │ Devices │ DHCP │ DNS │ VPN │ ...  │
├─────────────────────────────────────────────────┤
│              Saguaro Core (Go binary)           │
│   REST API v1  │  auth (token)  │  scheduler    │
├──────────────┬──────────────┬───────────────────┤
│  ubus (read) │  uci (write) │  SQLite (data)    │
├──────────────┴──────────────┴───────────────────┤
│ OpenWrt 25.12: netifd, dnsmasq, wireguard, fw4  │
└─────────────────────────────────────────────────┘
         LuCI (:443) ostaje paralelno, netaknut
```

## Slojevi

1. **Čitanje stanja** — isključivo `ubus` JSON pozivi (D-007)
2. **Pisanje konfiguracije** — isključivo `uci set/commit` + reload odgovarajućeg
   servisa (`ubus call service event` / init skripte) (D-008)
3. **Vlastiti podaci** (Inventory, korisnici, tokeni, audit log) — SQLite (D-006)

## Moduli i ovisnosti (redoslijed razvoja)

| # | Modul | Ovisi o | Opis |
|---|-------|---------|------|
| 1 | core | — | System info, health, API okvir, auth |
| 2 | web/dashboard | core | Prvi ekran (read-only) |
| 3 | inventory | core | Baza uređaja/klijenata — temelj svega |
| 4 | dhcp | inventory | Static leases iz inventorija → UCI dhcp |
| 5 | dns | inventory | Lokalni hostname zapisi → dnsmasq |
| 6 | wireguard | inventory | Peer management → UCI network |
| 7 | backup | inventory | sysupgrade -b + saguaro.db + rotacija |

## API v1 — nacrt (Core, faza 1: sve read-only)

| Endpoint | Izvor | Vraća |
|----------|-------|-------|
| `GET /api/v1/system` | `ubus call system board` | hostname, model, OpenWrt verzija, kernel |
| `GET /api/v1/system/status` | `ubus call system info` | uptime, load, RAM, swap |
| `GET /api/v1/storage` | statfs na /, /opt/saguaro | disk ukupno/slobodno |
| `GET /api/v1/interfaces` | `ubus call network.interface dump` + `ip -j link` | sučelja, IP, MAC, stanje, brzina |
| `GET /api/v1/health` | interno | gateway ping, DNS resolve, internet check |
| `GET /api/v1/identity` | SQLite + /proc | UUID uređaja, serial, lokacija (iz inventorija) |

Sve rute osim `/api/v1/health` (liveness) traže `Authorization: Bearer <token>`.

## Dashboard v1 — sadržaj prvog ekrana

Hostname, verzija (OpenWrt + Saguaro), CPU load, RAM, disk, uptime,
sučelja sa stanjem, gateway, internet status. **Ništa više** — "Services"
i "Updates" dolaze kad postoji modul koji ih zna popuniti.
