# Golden Base — ImageBuilder

Cilj: ponovljiv OpenWrt image za IN100 (x86_64, 25.12.x) umjesto ručno
složene kutije. Prvi uređaj smije biti ručno konfiguriran — image se
izgradi retroaktivno iz njegovog stanja (snimke u `docs/facts/`).

## Sadržaj (kad se popuni)

- `packages.txt` — popis paketa povrh defaulta (npr. `wireguard-tools`,
  `qrencode`, `block-mount`, `kmod-fs-ext4`, `luci-ssl`, `htop`, `tree`)
- `files/` — overlay koji ide u image:
  - `etc/config/network` — raspored portova (docs/NETWORK-PLAN.md)
  - `etc/config/firewall` — zone wan/lan/mgmt
  - `etc/init.d/saguaro-core` — procd init skripta
  - `etc/dropbear/authorized_keys` — SSH ključevi
- `build.sh` — poziv ImageBuildera s gornjim ulazima

## Napomena za 25.12

ImageBuilder za 25.12 koristi apk; provjeriti točan naziv arhive na
downloads.openwrt.org za target `x86/64`.
