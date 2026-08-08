# Gotova slika — ImageBuilder

Ponovljiva OpenWrt slika za IN100 (x86_64, 25.12.x) sa svim što treba unutra:
OpenWrt, paketi, Saguaro program i sučelje, init skripte, root particija 1 GB i
stvaranje data particije pri prvom dizanju. Slika je **glavni put isporuke**
(D-014) — uređaj kod korisnika ne treba internet.

## Sadržaj direktorija

- `build.sh` — preuzme službeni ImageBuilder (uz provjeru sha256 otiska),
  ubaci `packages.txt` i `files/`, izgradi sliku s rootom 1024 MB, pa joj da
  **vlastiti potpis diska** i uskladi `grub.cfg` (D-015). Poziv:
  `sh image/build.sh [verzija]` (zadano OpenWrt 25.12.5); verzija Saguara se
  čita iz `core/main.go`.
- `verify.sh` — otvori izgrađenu sliku i provjeri particije, potpis diska i
  prisutnost ključnih datoteka/alata; odbija sliku sa zadanim OpenWrt potpisom.
- `packages.txt` — popis paketa povrh defaulta (~180): dnsmasq-full, wireguard,
  openvpn, mwan3, banip, adblock-fast, bird2, sqm-scripts, ddns-scripts,
  nlbwmon, parted/partx/e2fsprogs/block-mount (data particija), qrencode (2FA),
  luci-ssl, i drugi.
- `files/` — overlay koji ide u sliku:
  - `etc/init.d/saguaro-core` — procd init skripta servisa
  - `etc/init.d/saguaro-datapart` — vraća zapis o data particiji nakon
    nadogradnje (uz provjeru ext4 potpisa)
  - `etc/uci-defaults/99-saguaro-firstboot` — prvo dizanje: zadana lozinka,
    stvaranje data particije, pokretanje servisa
  - `etc/profile.d/99-saguaro.sh` — podsjetnik na konzoli (adresa sučelja,
    `saguaro-setup`)
  - `usr/sbin/saguaro-setup` — konzolni čarobnjak (LAN adresa, instalacija na
    disk, reset lozinke sučelja)

> Mrežni raspon i firewall zone se **ne** prilažu kao overlay — uređaj dolazi
> na zadanom OpenWrt LAN-u (192.168.1.1), a adresa se postavlja konzolom ili
> kroz čarobnjak prvog postavljanja. (Raniji `board.d` pristup je namjerno
> uklonjen — vidi `docs/TESTOVI.md`.)

## Kako se gradi

GitHub Actions gradi sliku na svaki tag `vX.Y.Z` i objavljuje je uz izdanje;
probne grane `ci/**` i ručno pokretanje grade bez objave. Lokalno:

```sh
sh image/build.sh          # slika za zadanu OpenWrt verziju
sh image/verify.sh <slika> # provjera izgrađene slike
```
