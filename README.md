# Saguaro Infrastructure

Upravljačka platforma za IN100 appliance uređaje na bazi OpenWrt-a.
OpenWrt je "motor ispod haube" — Saguaro je korisničko iskustvo: web sučelje i
REST API na hrvatskom, koji rade **paralelno s LuCI-jem** (LuCI ostaje
netaknut). Saguaro dira samo zapise koje je sam stvorio (`sag_*`) i prije svake
primjene sprema backup konfiguracije.

Stanje: **v0.47.0**, 31 modul / 7 skupina, preko 200 API krajnjih točaka.

## Brzi početak

**Gotova slika (preporučeno):** uz svako izdanje ide
`saguaro-vX.Y.Z-openwrt-25.12.5-x86-64.img.gz` — sve unutra, uređaj ne treba
internet.

1. Napiši sliku na USB (Rufus, način *DD image*).
2. Digni uređaj s USB-a; na konzoli pokreni `saguaro-setup` (LAN adresa, po
   želji instalacija na disk).
3. Otvori `https://<IP>:8443/`, prijava `admin` / `Sgs#2026` — sučelje odmah
   traži novu lozinku i vodi kroz prvo postavljanje (ime, zona, lozinka
   uređaja, internet, LAN).

**Postojeći OpenWrt uređaj:**
```sh
wget -O - https://raw.githubusercontent.com/saguarogit-cmzk/SGSWRT/main/scripts/install.sh | sh
```

Potpune upute: [`docs/PRIRUCNIK.md`](docs/PRIRUCNIK.md).

## Gradnja

Razvoj je na radnoj stanici (Windows); na uređaj ide samo build.

```sh
sh scripts/build.sh                       # cross-compile saguaro-core (linux/amd64) + web u dist/
sh scripts/deploy.sh root@<ip-uređaja>    # deploy dist/ na uređaj
sh image/build.sh [verzija]               # gotova OpenWrt slika (ImageBuilder)
sh scripts/release.sh                      # priprema izdanja
```

GitHub Actions gradi sliku na svaki tag `vX.Y.Z` (probne grane `ci/**` grade
bez objave izdanja). Verzija se drži na jednom mjestu: `core/main.go`
(`const version`).

## Model

**Standalone (Opcija A):** Saguaro živi na svakom uređaju. Podaci su lokalna
SQLite baza. Shema je od prvog dana "sync-ready" (UUID po zapisu), tako da
kasniji prelazak na centralni kontroler ostaje moguć bez migracije.

## Ciljna platforma

- IN100 appliance: x86_64, 4× 1 GbE (Intel, e1000e), 8 GB RAM, 240 GB disk
- OpenWrt **25.12.x stable** — paketni manager **apk**, firewall fw4/nftables
- Saguaro se instalira u `/opt/saguaro` (zasebna data particija — preživljava
  sysupgrade)

## Struktura repozitorija

```
core/       Saguaro Core (Go) — čita stanje (ubus/uci), servira REST API i web
web/        Sučelje (index.html + app.js + style.css), servira ga core
scripts/    build, deploy, install, release, selftest, device-facts
image/      ImageBuilder gradnja gotove slike (build.sh, verify.sh, packages.txt, files/)
docs/       Priručnik, mogućnosti, testovi, odluke (DECISIONS.md), revizije
dist/       Rezultat builda (ne u gitu)
```

## Pravila razvoja

1. Razvoj na radnoj stanici, na uređaj ide **deploy** — nikad se ne razvija
   direktno na routeru.
2. Core **ne parsira izlaze naredbi** — isključivo `ubus` (JSON) i `uci`.
3. Saguaro **ne zamjenjuje** OpenWrt servise (dnsmasq, wireguard, fw4) — njima
   **upravlja** kroz UCI, i dira samo vlastite `sag_*` sekcije.
4. LuCI ostaje netaknut kao servisni ulaz.
5. Sve na hrvatskom — sučelje, dokumentacija, commit poruke; ništa se ne
   proglašava gotovim dok nije provjereno na stvarnom uređaju
   (`docs/TESTOVI.md`).

Ključne odluke i njihova obrazloženja: [`docs/DECISIONS.md`](docs/DECISIONS.md).
