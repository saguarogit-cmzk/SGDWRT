# Hardverski profil — IN100 (snimljeno 2026-07-29, uređaj 192.168.30.222)

| Stavka | Vrijednost |
|--------|-----------|
| CPU | Intel Atom E3845 @ 1.91 GHz, 4 jezgre (Bay Trail) |
| RAM | 8 GB (bez swapa) |
| Disk | 234 GB (sda), **root particija sda2 preko cijelog diska**, ext4, 220.6 GB slobodno |
| NIC | 4× Intel **1 GbE** (driver e1000e) — eth0–eth3, MAC 00:e0:67:08:18:18–1b |
| OS | OpenWrt 25.12.4 (r32933), kernel 6.12.87, target x86/64 |
| Paketi | 203 instaliranih, LuCI prisutan |

## Trenutno mrežno stanje ("lab mode")

- `br-lan` = eth0, statički **192.168.30.222/24**, gateway/DNS 192.168.30.1
  → uređaj je klijent iza postojećeg routera; jedini kabel je u **portu eth0**
- `wan` = eth1 (dhcp), bez kabela; eth2/eth3 down
- dnsmasq: default konfiguracija, DHCP server aktivan na lan (100–250)

**Za razvoj se lab mode ne dira** — uređaj ostaje klijent na 192.168.30.222.
Prebacivanje u ciljni raspored portova (WAN/MGMT/LAN iz NETWORK-PLAN.md) radi
se tek pri stvarnoj instalaciji na lokaciju, po pisanoj proceduri, jer mijenja
put pristupa uređaju.

## Napomene

- BusyBox `ip` nema `-j` (JSON) — Core za sučelja koristi `ubus call
  network.device status` + sysfs (`/sys/class/net/*`), ne `ip` naredbu.
- E3845 ima AES-NI → WireGuard i HTTPS bez problema za 1 GbE klasu.
- Pristup: SSH ključem (`~/.ssh/id_ed25519` s radne stanice, instaliran
  2026-07-29). Lozinku promijeniti i potom isključiti password auth na
  dropbearu.
