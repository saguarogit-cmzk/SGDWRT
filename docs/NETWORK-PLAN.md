# Mrežni plan — IN100 (4× 2.5GbE)

## Raspored portova (fiksan — nikad se ne mijenja)

| Port | Interface | Uloga | Napomena |
|------|-----------|-------|----------|
| 1 | eth0 | **WAN** | DHCP client ili PPPoE prema ISP-u |
| 2 | eth1 | **MGMT** | Management — statični 192.168.100.1/24, uvijek dostupan SSH/LuCI/Saguaro. "Izlaz u nuždi". |
| 3 | eth2 | LAN (bridge `br-lan`) | Kasnije VLAN trunk prema switchu |
| 4 | eth3 | LAN (bridge `br-lan`) | |

> Stvarna imena sučelja provjeriti na uređaju (`ip -j link`) — i226-V se na
> x86 OpenWrt-u obično enumerira kao eth0–eth3, ali redoslijed portova na
> kućištu treba potvrditi kabelom prije nego se plan uklesa.

## UCI predložak (`/etc/config/network` — relevantni dio)

```
config interface 'wan'
        option device 'eth0'
        option proto 'dhcp'

config interface 'mgmt'
        option device 'eth1'
        option proto 'static'
        option ipaddr '192.168.100.1'
        option netmask '255.255.255.0'

config device
        option name 'br-lan'
        option type 'bridge'
        list ports 'eth2'
        list ports 'eth3'

config interface 'lan'
        option device 'br-lan'
        option proto 'static'
        option ipaddr '192.168.1.1'
        option netmask '255.255.255.0'
```

## Firewall smjernice (fw4)

- Zone: `wan` (input/forward REJECT), `lan` (ACCEPT), `mgmt` (ACCEPT, ali bez forwarda prema lan/wan osim po potrebi)
- SSH: samo ključem (`/etc/dropbear/authorized_keys`), lozinke isključiti nakon što ključ radi
- LuCI (:443) i Saguaro (:8443): dostupni samo iz `mgmt` i `lan`, nikad iz `wan`
- Saguaro API sluša na 0.0.0.0:8443, ali fw4 pravila ograničavaju pristup po zoni

## Recovery (Dan 0 — obavezno testirati PRIJE eksperimenata)

1. MGMT port (eth1) sa statičnim IP-em je primarni put spašavanja — laptop direktno na port 2, ručni IP 192.168.100.2/24.
2. OpenWrt **failsafe mode** — provjeriti kako se aktivira na x86 (tipkovnica + monitor na boot ili serijska konzola ako je IN100 ima).
3. USB stick s instalacijskim imageom 25.12.4 — uvijek pripremljen u ladici.
4. Prije svake veće izmjene: `sysupgrade -b /tmp/backup-$(date +%F).tar.gz` i kopija van uređaja.
