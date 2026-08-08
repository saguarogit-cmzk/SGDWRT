# Testiranje uređaja — Faza D

Uz svaku isporuku ide `selftest.sh` — samoprovjera koja se pokreće **na uređaju**:

```sh
sh /opt/saguaro/selftest.sh              # samo čitanje, ništa ne mijenja
sh /opt/saguaro/selftest.sh --disruptive # uz to gasi OpenVPN i gleda vraća li se sam
```

Provjerava **stvarno stanje** (nftables, procesi, portovi, ubus), ne samo
konfiguraciju — jer "postoji u postavkama" i "radi" nisu isto. Izlazni kod je
0 ako je sve prošlo, 1 ako je bilo padova, pa se može zvati i iz nadzora.

Ispis ima tri oznake:

- **PROŠLO** — provjereno naredbom na uređaju.
- **PALO** — nešto stvarno ne valja; uz redak piše kako se popravlja.
- **PRESKAČEM** — nije greška: funkcija nije uključena ili se ne može provjeriti
  bez vanjskog resursa (stvarni VPN klijent, poštanski poslužitelj...).

---

## Rezultat na testnom uređaju (2. 8. 2026., v0.21.0)

**37 prošlo · 0 palo · 4 preskočeno**

Pokriveno automatski: servis i samopokretanje, API s tokenom i bez njega,
sigurnosna zaglavlja, odbijanje zastarjelog TLS-a, internet, DNS, DNSSEC,
zadana ruta, vremenska zona, politike firewalla, poklapanje konfiguracije i
jezgre, zatvorenost upravljanja prema internetu, WireGuard, OpenVPN (uključivo
odustajanje od root ovlasti, CRL i propisana kriptografija), zatvorenost VPN
zona prema upravljanju, banIP na pravom sučelju, blokada domena, očvršćivanje,
trajni logovi i rotacija, ispravnost backup arhive, preživljavanje nadogradnje
firmwarea, samostalni oporavak servisa.

---

## Testovi izvedeni ručno u Fazi D

### Restart uređaja (test 21)

Najvažniji dosad neizvedeni test. Snimljeno stanje prije, uređaj ponovno
pokrenut, pa uspoređeno.

- Uređaj se vratio za **~55 sekundi**.
- **Sve postavke identične prije i poslije**: `rp_filter=2`, ograničenje pinga
  10/s, LuCI na HTTPS, DNS ne sluša na WAN-u, trajni logovi, uklonjena IPsec
  pravila, obje VPN zone na REJECT, 4 zone u nftablesu, 2 cron zadatka, CRL,
  16 495 banIP zapisa.
- **Upozorenje o restartu okinulo je samo od sebe** i zapisano je u dnevnik:
  *"Uređaj se ponovno pokrenuo (radi 40 sekundi)."*
- Zapamćeno stanje (javna adresa, CGNAT, stanje servisa) preživjelo je restart
  jer živi u bazi, ne u memoriji.
- Sve provjere iz `selftest.sh` prošle su i nakon restarta.

### Prebacivanje između internet veza (test 14) — regresija nakon Faze C

U Fazi C je uključen `rp_filter`, koji u strogom načinu razbija multi-WAN. Zato
je failover ponovno provjeren:

- Isključena primarna veza → promet je prešao na pričuvnu (`lan (100%)`),
  internet i DNS radili su bez prekida.
- Vraćena primarna → promet se vratio na nju, zadana ruta ponovno preko WAN-a.
- **Zaključak: `rp_filter=2` (labavo) ne smeta failoveru.** Da je postavljeno
  na 1 (strogo), ovaj bi test pao — zato se namjerno postavlja 2.

### Samostalni oporavak servisa (test 23)

`killall openvpn` → procd ga je sam vratio unutar 8 sekundi.

### Prva prijava na svježoj instalaciji

Pokrenuta zasebna instanca s praznom bazom i zadanom lozinkom kakvu sije
`install.sh`, pa provjeren cijeli tijek:

1. Prijava s `admin` / `Sgs#2026` → prolazi, uz zastavicu
   `must_change_password: true`.
2. Poziv bilo koje druge funkcije → **403** (blokirano do promjene lozinke).
3. Promjena lozinke → prolazi.
4. Ista funkcija sada → **200**.
5. Stara zadana lozinka → **odbijena**.

Testna instanca je nakon toga ugašena i obrisana.

### Instalacijski paket

Sadržaj `saguaro-v0.21.0-linux-amd64.tar.gz` odgovara točno onome što
`install.sh` traži (`saguaro-core`, `web/`, `init.d-saguaro-core`,
`selftest.sh`) — svježa instalacija ne može pasti na nedostajućoj datoteci.

---

## Nalaz otkriven testiranjem

**Nadogradnja firmwarea gubila bi Saguaro postavke.**

`sysupgrade -b` čuva `/etc/sysctl.conf`, ali **ne** `/etc/sysctl.d/`, a ni
`/opt` — pa bi se pri nadogradnji firmwarea (ili vraćanju backupa na svjež
sustav) izgubili: očvršćivanje jezgre, API token, TLS certifikat, OpenVPN PKI,
ključ za slanje backupa i cijela baza.

Riješeno: Saguaro pri pokretanju zapisuje `/lib/upgrade/keep.d/saguaro` s
popisom `/etc/sysctl.d/99-saguaro.conf`, `/opt/saguaro/etc` i
`/opt/saguaro/data`. Provjereno — `sysupgrade -l` ih sada nabraja. Backup
arhive i logovi namjerno **nisu** na popisu (bilo bi kružno odnosno
nepotrebno veliko). Samoprovjera od sada to i provjerava.

---

## Što još nije dokazano

Ovo se ne može provjeriti bez vanjskih resursa. Dok se ne izvede, za te
funkcije vrijedi: *konfiguracija postoji, ali funkcionalnost nije praktično
verificirana.*

| Test | Što treba | Kako provjeriti |
|---|---|---|
| **WireGuard klijent** | stvarni uređaj s WireGuard aplikacijom | Dodaj korisnika → *Config* → uvezi u aplikaciju → spoji se. Zatim `wg show` mora pokazati **latest handshake** unutar 2 min. Samoprovjera to od tada javlja sama. |
| **OpenVPN klijent** | uređaj s OpenVPN aplikacijom | Isto; `/tmp/sag_ovpn.status` mora dobiti redak koji počinje s `CLIENT_LIST`. |
| **Pravila po VPN korisniku** | spojen VPN klijent | S klijenta kucni na **dopušteno** odredište (mora proći) i na **nedopušteno** (ne smije proći). |
| **VPN → upravljanje** | spojen VPN klijent | S klijenta `ssh 10.6.0.1` mora biti **odbijen**. Pravila su provjerena u nftablesu, ali ne i s klijenta. |
| **E-mail obavijesti** | poštanski račun | Nadzor → SMTP postavke → *Pošalji probnu poruku*. Koristi zaseban račun i lozinku aplikacije. |
| **Backup izvan uređaja** | server ili NAS sa SSH-om | Backup → upiši odredište → javni ključ prekopiraj na server → *Pošalji zadnju arhivu odmah*. Prijenos i šifriranje su dokazani preko povratne petlje, ali ne prema stvarnom odredištu. |
| **DDNS** | račun kod pružatelja | Mreža → DDNS → nakon spremanja `logread \| grep ddns` mora pokazati uspješno osvježavanje. |
| **Vanjski syslog** | syslog poslužitelj | `logger -t test proba` pa provjera na poslužitelju. |
| **Vraćanje backupa** | najbolje drugi uređaj | Vraćanje prepisuje konfiguraciju i restarta uređaj. Ciklus je dokazan u ranijoj sesiji, ali ne s trenutnom verzijom. |
| **Nestanak struje** | fizički pristup | Isključi napajanje na 30 s. Uređaj se mora podići; `ext4` je otporan, ali UPS je preporuka. |
| **Objava servera izvana** | javna IP adresa | Uređaj je trenutačno iza operaterskog/tuđeg NAT-a (samoprovjera to javlja), pa se objave izvana **ne mogu** testirati na ovoj lokaciji. |

---

## Redoslijed za prvu instalaciju kod klijenta

1. `install.sh`, pa prijava `admin` / `Sgs#2026` → sučelje odmah traži novu lozinku.
2. **Postavke**: lozinka uređaja (root), vremenska zona, očvršćivanje.
3. **Mreža**: WAN, pa dodatne mreže (VLAN-ovi, DMZ na portu).
4. **Firewall**: objava servera, uz split DNS ako se pristupa imenom.
5. **VPN**: korisnici, pa **stvarno spajanje klijenta** (bez toga nije dokazano).
6. **Nadzor**: SMTP + probna poruka, pa uključi upozorenja.
7. **Backup**: lozinka za šifriranje (**zapiši je**), pa barem jedan put izvan
   uređaja — poslužitelj/NAS ili slanje na e-mail (može i oboje). Bez toga
   samoprovjera javlja PALO, i s pravom.
8. `sh /opt/saguaro/selftest.sh` — sve mora biti PROŠLO ili PRESKAČEM.
9. Puni backup i **preuzmi ga na svoje računalo**.

## Let's Encrypt za obrnuti proxy (v0.29.0)

Provjereno na uređaju **03.08.2026.**:

| Korak | Ishod |
|---|---|
| Putanja za provjeru kroz proxy | **radi** — `GET /.well-known/acme-challenge/<token>` na portu 8080 vraća sadržaj iz mape paketa (`/var/run/acme/challenge`) |
| Druge putanje prema tom poslužitelju | **odbijene** — 503, proxy propušta isključivo putanju provjere |
| Zapis u `/etc/config/acme` | **radi** — sekcija `sag_<id>` s domenom, `validation_method=webroot`, `key_type=ec256`, `staging` |
| Pokretanje izdavanja | **radi** — `acme.sh` se pokreće s našim e-mailom prema **staging** poslužitelju Let's Encrypta |
| Odgovor certifikacijskog tijela | **stigao** — `rejectedIdentifier` za `test-le.example.com` (LE odbija ogledne domene) |
| Ispis postupka natrag u sučelje | **radi** — zadnji redci iz syslog-a vraćaju se u odgovoru |

**Što se ovdje ne može dokazati:** stvarno izdavanje certifikata. Uređaj je iza
operaterskog NAT-a (CGNAT) i nema javnu domenu koja pokazuje na njega, a
Let's Encrypt mora doći na port 80 tog imena. Cijeli lanac do certifikacijskog
tijela je prošao i dobio odgovor — nedostaje samo javno dostupno ime.

Za provjeru na lokaciji s javnom adresom: dodaj stranicu s **probnim
poslužiteljem (staging)**, zatraži certifikat i pogledaj ispis. Kad staging
prođe, isključi ga i zatraži pravi.

## Nadogradnja OpenWrt-a 25.12.4 → 25.12.5 (03.08.2026.)

Provedena stvarna nadogradnja kroz sučelje, na uređaju u pogonu.

| Korak | Ishod |
|---|---|
| Slika naručena s popisom paketa uređaja (178) | **radi** — servis vratio `ext4-combined` (BIOS), 30,6 MB |
| SHA256 pri preuzimanju i prije upisa | **radi** — `sha256sum` na uređaju odgovara otisku servisa |
| Puni backup prije upisa | **radi** — arhiva napravljena automatski i preuzeta na radnu stanicu |
| `sysupgrade` odvojen od HTTP zahtjeva | **radi** — odgovor je stigao, uređaj se digao za ~1 min |
| Verzija nakon dizanja | **25.12.5** (r33051-f5dae5ece4), jezgra 6.12.94 |
| Paketi | **svih 178 preživjelo** — provjera „nedostaje" prazna |
| Konfiguracija (firewall, VPN, DHCP, banIP, detekcija skeniranja) | **preživjela** — OpenVPN klijent, banIP i 7 `sag_scan` pravila na mjestu |
| Saguaro baza i certifikati | **preživjeli** |

**Dva propusta koja je ova nadogradnja otkrila** (oba popravljena u v0.29.2):

1. Keep lista nije čuvala `/etc/rc.d/S95saguaro-core`, pa se servis nakon
   dizanja **nije sam pokrenuo** — sučelje je bilo nedostupno dok se ne pokrene
   ručno (SSH je radio cijelo vrijeme).
2. Keep lista je nabrajala pojedine mape, pa su **arhive backupa i skripta
   samoprovjere nestale**. Sad se čuva cijeli `/opt/saguaro`.

Nakon popravka keep liste i nove arhive: samoprovjera **41 prošlo / 0 palo**.

## Root particija nakon nadogradnje (04.08.2026.) — **kvar i pouka**

Nadogradnja 25.12.4 → 25.12.5 vratila je root particiju s 220 GB na
**104 MB** (disk je 234 GB), jer x86 slika nosi i tablicu particija. Disk je
odmah bio zauzet 98 %.

**Što je pokušano i zašto nije prošlo:**

| Pokušaj | Ishod |
|---|---|
| `resize2fs` na živoj montiranoj particiji | **ne radi** — jezgra javlja `reserve_backup_gdb` i staje na 128 MB |
| Postupno širenje (512 MB, pa 4 GB) | **ne radi** — ista greška |
| Službeni `expand-root` (`/etc/uci-defaults/70-rootpt-resize` + `80-rootfs-resize`) | **uništio datotečni sustav** — uređaj se nakon restarta nije digao, konzola javlja grešku ext4 grupa |

Uzrok: `80-rootfs-resize` radi `resize2fs -f` preko **loop uređaja** nad
particijom koja je istovremeno montirana kao root za pisanje. Na `squashfs`
slikama to mijenja odvojeni overlay i bezopasno je; na **ext4 kombiniranoj
slici to je isti datotečni sustav s dvije strane**. Postupak je za našu vrstu
slike neispravan iako ga wiki navodi kao općenit.

**Ispravno rješenje (v0.30.0):** veličina se zadaje pri izgradnji slike.
Provjereno na stvarnoj slici:

| Provjera | Ishod |
|---|---|
| Servis prima `rootfs_size_mb` | **radi** — `openapi.json` navodi raspon 1–1024 MB |
| Naručena slika 25.12.5 sa 178 paketa i `rootfs_size_mb: 1024` | **radi** — izgrađena, 31,5 MB, otisak odgovara |
| Tablica particija u samoj slici (očitan MBR) | **1024,0 MB** za `sda2` (prije 104 MB) — parametar stvarno djeluje |
| Odbijanje premale slike pri naručivanju (10 MB uz 75 MB zauzeto) | **radi** — HTTP 409 s objašnjenjem |
| Odbijanje veličine preko granice servisa (4096 MB) | **radi** — HTTP 400 |
| Uređaj naručuje sliku s `rootfs_size_mb` | **radi** — servis izgradio sliku, odgovor nosi `rootfs_mb: 1024` |
| Tablica particija u slici koju je naručio **uređaj** (očitan MBR) | **1024,0 MB** — potvrđeno na kraju lanca, ne samo na radnoj stanici |
| Upozorenje se veže na **traženu**, ne na prijašnju veličinu | ugrađeno — namjerno smanjenje (220 GB → 1 GB) ne diže lažnu uzbunu |
| `GET /api/v1/openwrt/disk` na uređaju | **radi** — `sda2`, 226 GB, slobodno 225 GB, stanje `ok` |
| Samoprovjera nakon dopune | **42 prošlo / 0 palo** |
| Odbijanje premale slike **prije upisa** i potvrda za sliku s računala | ugrađeno; **nije provjereno na uređaju** — provjera bi tražila stvarni upis slike, što se na uređaju u pogonu ne radi |
| Usporedba veličine nakon dizanja i e-mail upozorenje | ugrađeno; **provjerit će se pri sljedećoj stvarnoj nadogradnji** |

### Kako je uređaj oporavljen

Uređaj se ipak digao sam — proširenje je dovršeno, ali je trajalo oko **12 sati**
(`resize2fs` gradi metapodatke za 234 GB na Atomu E3845). Sve je preživjelo:
konfiguracija, Saguaro baza, certifikati, `/opt/saguaro` u cijelosti; jezgra
javlja nula `EXT4-fs error`. Root je sada 220,7 GB.

To ne mijenja odluku D-012: postupak je i dalje neprihvatljiv jer uređaj u
pogonu ne smije biti pola dana nedostupan, a ishod je bio neizvjestan.
Skripte `70-rootpt-resize` i `80-rootfs-resize` maknute su iz
`/etc/uci-defaults/` u `/root/expand-root-onemoguceno/` da se ne mogu ponovno
pokrenuti.

> Napomena za sljedeću nadogradnju: nova slika nosi root od 1024 MB, pa će se
> particija smanjiti s 220,7 GB na 1 GB. To je namjerno — sustav troši 75 MB, a
> ostatak diska ima smisla samo kao zasebna particija za podatke.

Vidi odluku D-012.

## Slanje backupa e-mailom (v0.31.0)

| Provjera | Ishod |
|---|---|
| Sastavljanje MIME poruke s privitkom | **radi** — poruka se raspakira natrag (`net/mail` + `mime/multipart`), privitak bajt-u-bajt jednak, naslov s hrvatskim znakovima ispravno kodiran (`TestBuildMailWithAttachment`) |
| Šifriranje pa vraćanje arhive | **radi** — sadržaj se ne pojavljuje u šifriranoj datoteci, kriva lozinka odbijena (`TestBackupMailEncryptRoundTrip`) |
| Lozinka nije nigdje u poruci | **provjereno testom** — poruka se pretražuje na lozinku i mora je ne sadržavati |
| Slanje bez SMTP-a | **radi** — HTTP 502 uz „SMTP poslužitelj nije postavljen (Nadzor → E-mail)" |
| Uključivanje bez SMTP-a ili bez lozinke arhive | odbija se s HTTP 409 |
| `GET /api/v1/backup/mail` na uređaju | **radi** — `pass_set: true`, `smtp_ready: false`, granica 15 MB |
| Samoprovjera javlja da kopija ne izlazi s uređaja | **radi** — 42 prošlo / **1 palo** (namjerno: ni poslužitelj ni e-mail nisu uključeni) |
| **Stvarna isporuka poruke** | **radi** — nakon što je korisnik unio SMTP (lin10.croadria.com:587, STARTTLS), poslužitelj je prihvatio poruku s privitkom `full-20260804-034810.tar.gz.enc` ; **primatelj potvrdio da je poruka stigla** (04.08.2026.). Otvaranje privitka naredbom `-decrypt-backup` nije zasebno potvrđeno. |
| Automatsko slanje uz noćni backup | **radi** — uz `always` novi backup vrati `"mail":"poslano"` |
| Učestalost slanja (uz svaki / dnevno / tjedno / mjesečno) | **radi** — uz `weekly` i već poslanu poruku isti dan, novi backup vrati `"mail":"preskočeno (jednom tjedno)"`; nepoznata vrijednost odbijena s HTTP 400 |
| Samoprovjera nakon uključenja | **44 prošlo / 0 palo** |

## Statičke rute (v0.32.0)

Provjereno na uređaju **04.08.2026.**:

| Provjera | Ishod |
|---|---|
| Čitanje tablice usmjeravanja iz jezgre | **radi** — `/proc/net/route` i `/proc/net/ipv6_route`; obje zadane rute multi-WAN-a (metrika 10 i 20), WireGuard i OpenVPN mreže, IPv6 zapisi |
| Zadana ruta `0.0.0.0/0` | **odbijena**, HTTP 400 — nju vodi internet veza odnosno Multi-WAN |
| Gateway izvan mreže sučelja | **odbijen**, HTTP 400 uz popis mreža tog sučelja (jezgra bi takvu rutu odbila tiho) |
| Nepostojeće sučelje | **odbijeno**, HTTP 400 |
| Dodavanje i primjena | **radi** — `network.sag_rt_01e28fea` sa `interface`, `target`, `gateway`, `metric`; u jezgri `192.168.100.0/24 via 192.168.50.1 dev br-lan proto static metric 5` |
| Safe mode pri primjeni | **radi** — primjena vrati `safe_mode: true`, potvrda kroz `/rollback/confirm` |
| Isključivanje rute pa primjena | **radi** — ruta nestala iz jezgre |
| Brisanje pa primjena | **radi** — nula zaostalih `sag_rt_` zapisa u `/etc/config/network` |
| Samoprovjera | **44 prošlo / 0 palo** (nova provjera uspoređuje upisane rute s tablicom jezgre) |

Testna ruta je nakon provjere obrisana — uređaj je ostao bez ijedne statičke rute.

## Prisilni DNS i vremenska pravila (v0.33.0)

Provjereno na uređaju **04.08.2026.**:

| Provjera | Ishod |
|---|---|
| fw4 prihvaća vremenska ograničenja | **radi** — `meta hour "08:00:00"-"18:00:00" meta day { "Monday", … }` |
| fw4 i lista negiranih adresa u `redirect` | **ne radi** — `option 'src_ip' must not be a list` (ista granica kao kod SNAT-a) |
| Iznimke preko imenovanog skupa (`config ipset`) | **radi** — nastane `ip saddr != @sag_fdns_skip`, bez ograničenja broja iznimki |
| Prisilni DNS uključen na `lan` | **radi** — u jezgri sva tri sloja: `tcp/udp dport 53 … redirect to :53`, `tcp dport 853 … reject`, `ip daddr { … } tcp dport 443 … reject` |
| Skup iznimki | **radi** — `elements = { 192.168.50.10 }` |
| Zona koja nije lokalna (`wan`) | **odbijena**, HTTP 400 |
| Uključeno bez odabrane mreže | **odbijeno**, HTTP 400 |
| Neispravna iznimka | **odbijena**, HTTP 400 |
| Isključivanje i primjena | **radi** — nula zaostalih `sag_fdns_` zapisa, nula pravila u jezgri |
| **Stvarni pokušaj zaobilaženja s klijenta** | **nije provjereno** — radna stanica na 192.168.50.75 ima gateway 192.168.50.1, dakle njen promet ne prolazi kroz ovaj uređaj, pa se preusmjeravanje ne može okinuti. Treba klijent kojem je ovaj uređaj gateway: `nslookup example.com 8.8.8.8` mora dobiti odgovor od uređaja, a brojač na redirect pravilu porasti. |

Prisilni DNS je nakon provjere **isključen** — uređaj je vraćen u stanje prije testa.

## Data particija (v0.34.0)

Provjereno na uređaju **04.08.2026.**:

| Provjera | Ishod |
|---|---|
| Alati na uređaju | **ima ih** — `parted`, `mkfs.ext4`, `partx`, `losetup`, `resize2fs`, `e2fsck`, `block-mount`. Nema `blkid` ni `dumpe2fs`, pa se UUID **zadaje** pri `mkfs.ext4 -U` umjesto da se poslije čita |
| Čitanje tablice particija | **radi** — `sda1` 16 MB od sektora 512, `sda2` 223,5 GB od sektora 33792 |
| Prepoznavanje da zahvat još nije moguć | **radi** — „root particija zauzima gotovo cijeli disk (228920 MB)… Prvo nadogradi OpenWrt" + tri koraka redom |
| Sigurnosna brana init skripte (ext4 potpis) | **radi** — `53ef` na početku `sda1` i `sda2`, `0000` na praznom mjestu u sredini diska, `c012` unutar MBR područja. Tablica se dira **samo** uz točan potpis |
| Init skripta postavljena i uključena | **radi** — `/etc/rc.d/S15saguaro-datapart` (prije `fstab`, koji je START=20) |
| **Sam zahvat (stvaranje particije i selidba)** | **nije izveden** — traži prethodnu nadogradnju OpenWrt-a koja oslobađa prostor. Čeka odluku korisnika. |

### Zašto zahvat traži nadogradnju

`sda2` zauzima cijeli disk, pa za `sda3` nema mjesta. Oslobađanje bi tražilo
**offline smanjivanje ext4**, a upravo je to 04.08.2026. oborilo uređaj na 12
sati (vidi D-012). Umjesto toga posao radi sama nadogradnja: nova slika nosi
tablicu s rootom od 1024 MB i time oslobađa ~222 GB — bez ijednog `resize2fs`.

## Gotova slika i prvo postavljanje (v0.35.0)

| Provjera | Ishod |
|---|---|
| ImageBuilder za 25.12.5 postoji i ima objavljen otisak | **radi** — `openwrt-imagebuilder-25.12.5-x86-64.Linux-x86_64.tar.zst`, sha `313221253d9bac53…` |
| Popis paketa snimljen s uređaja u pogonu | **180 paketa**; verzijska ograničenja maknuta, `dnsmasq` izrijekom maknut jer ga zamjenjuje `dnsmasq-full` |
| Sintaksa `image/build.sh` i skripte prvog dizanja | **prošla** (`sh -n`) |
| Endpoint za ime uređaja | **radi** — krivo ime odbijeno s HTTP 400, ispravno prihvaćeno |
| Čarobnjak prvog postavljanja u sučelju | složen: lozinka → ime uređaja, vremenska zona, LAN adresa; smoke test prolazi |
| Pokretanje gradnje bez objave izdanja | **radi** — grana `ci/**` gradi sve i ostavlja artefakte, korak *Release* se preskače (vezan na tag) |
| Prva gradnja | **pala** u koraku *Package*: naziv paketa uzima ime refa, a grana ga ima s kosom crtom (`ci/image-test`) pa `tar` puca na nepostojećem direktoriju. Popravljeno zamjenom `/` u `-`. |
| **Gradnja slike u GitHub Actions** | **radi** — svih devet koraka prošlo, artefakt `saguaro-build` 39,9 MB, korak *Release* preskočen jer nije tag |
| **Sadržaj slike (`image/verify.sh`)** | **radi** — provjera se izvodi u samoj gradnji: MBR (root 1024 MB), montiranje root particije i postojanje `saguaro-core` (izvršan), `index.html`, `app.js`, `style.css`, `selftest.sh`, obje init skripte, skripta prvog dizanja te alati `parted`, `mkfs.ext4`, `wg`, `openvpn` |
| Objavljeno izdanje v0.35.0 | **radi** — slika 35,5 MB na stranici izdanja; skinuta i provjerena neovisno o CI-ju: otisak odgovara, MBR potpis 55aa, particija 1 16 MB, particija 2 **1024 MB** |
| **Dizanje uređaja iz te slike** | **nije provjereno** — traži USB i fizički pristup |

### Tri pada prije nego je prošlo (svi u alatima, nijedan u slici)

1. *Package* — naziv datoteke uzima ime refa, a grana ga ima s kosom crtom
   (`ci/image-test`) pa `tar` puca. Popravljeno zamjenom `/` u `-`.
2. *Provjeri sliku* — `[ uvjet ] && var=…` uz `set -e` obori skriptu čim uvjet
   nije istinit, a nije već na prvoj particiji. Zamijenjeno s `if`.
3. *Provjeri sliku* — `find -type f` nije nalazio `mkfs.ext4` jer je simbolička
   poveznica na `mke2fs`. Uvjet `-type f` maknut.

Sama slika je bila ispravna u sva tri pokušaja. Da bi se ubuduće izbjeglo
zaključivanje posredno, razlog pada se sada ispisuje kao GitHub *annotation* —
logovi javnog repozitorija se bez prijave ne mogu čitati, a annotationi mogu.

## Dizanje s USB-a na starom hardveru (05.08.2026.)

Slika je napisana na USB 3.0 stick (Rufus, DD) i stavljena u IN100. **Uređaj se
nije digao sa sticka** ni nakon što je u BIOS-u isključen HDD boot.

Što je provjereno da NIJE uzrok:

| Provjera | Nalaz |
|---|---|
| Sadržaj sticka | ispravan — `saguaro-core` (izvršan), sučelje, `selftest.sh`, obje init skripte, skripta prvog dizanja; montirano i pregledano s uređaja |
| Tablica particija na sticku | `sdb1` 16 MB (zastavica `80`, boot), `sdb2` 1024 MB — točno kao slika |
| Boot kod u MBR-u slike | **GRUB je tu** — prva 3 bajta `eb 63 90`, tekst `GRUB `, `Geom`, `Hard Disk`, `Read`, ` Error`, `loading`, potpis `55 aa` |

Preostaje BIOS: stari uređaj ne diže s **USB 3.0** sticka. Za isporuku treba
USB 2.0 stick odnosno USB 2.0 port, uz *Legacy USB Support* u BIOS-u.

### Dva prava nalaza usput

1. **Isti potpis diska i isti UUID.** Interna instalacija i naša slika potječu
   iz iste OpenWrt slike, pa oba diska imaju potpis `e82a2d68` i root UUID
   `ff313567-…`. Jezgra root traži preko `root=PARTUUID=682d2ae8-02`, što
   odgovara **objema** particijama — pa uzme prvu, a to je interni disk. Dakle
   i da se GRUB digao sa sticka, root bi došao s diska.
   Zaobiđeno tako da je sticku promijenjen potpis (`61adf1fb`) i usklađen
   `grub.cfg` (`PARTUUID=fbf1ad61-02`). Kopija izvornog MBR-a i `grub.cfg` je
   na uređaju u `/root/usbfix/`.
   Uz to: dok je stick bio u uređaju, sustav je zbog istog UUID-a montirao
   **njegovu** boot particiju na `/boot` umjesto svoje — a kad je stick izvučen,
   `/boot` je ostao visjeti u prazno. Vraćeno ručno.
2. **Pogrešan trag: raspored portova.** U slici sam našao
   `ucidef_set_interfaces_lan_wan "eth1" "eth0"` i zaključio da OpenWrt na x86
   LAN stavlja na drugi port. **Netočno** — taj redak pripada nekom drugom
   uređaju u `02_network`. Konfiguracija koju je slika stvarno stvorila na
   uređaju ima `br-lan` s portom `eth0` i `wan` na `eth1`, dakle LAN je na
   prvom portu, kako i treba. Dodana `board.d` datoteka je zato uklonjena —
   rješavala je nepostojeći problem.

### Što je dizanje s USB-a ipak dokazalo

Nakon povratka na interni disk vidjelo se da je USB sustav **stvarno odradio
prvo dizanje**:

| Provjera | Nalaz |
|---|---|
| Data particija | `sdb3` **27,2 GB** stvorena iz slobodnog prostora, bez ijednog `resize2fs` |
| Selidba `/opt/saguaro` | prošla — `bin`, `web`, `data`, `etc`, `log`, `backup`, `selftest.sh` na novoj particiji |
| `fstab` zapis | `sag_data` → `/opt/saguaro`, uuid `a94ba422-…` |
| Zapis geometrije | `/etc/saguaro-datapart.json` upisan (disk `sdb`, particija 3, start 2131968, 58314752 sektora) |
| **Saguaro se pokrenuo** | baza `saguaro.db` stvorena, WAL 791 KB |
| `uci-defaults` | prazan — sve skripte se izvršile i uredno obrisale |
| Mrežna konfiguracija | primijenjena u cijelosti (adresa, maska, gateway, DNS) |

Dakle **prvo dizanje iz gotove slike radi na stvarnom hardveru**, uključivo
stvaranje data particije i selidbu podataka. Ono što nije radilo je sam USB 3.0
stick na tom starom kontroleru: konzola je javljala da ne može pokrenuti
`/usr/libexec/login.sh`, a sustav je prestao odgovarati na mreži ubrzo nakon
pokretanja — što odgovara greškama čitanja s medija, ne s pogreškom u slici.

## Jedinstven potpis diska u slici (v0.36.1)

Prvi pokušaj (v0.36.0) **pao je na provjeri slike**, i to je provjera i uhvatila:

```
::error::GRUB u slici ne traži PARTUUID 5c5c5c5c — potpis i grub.cfg nisu usklađeni
```

`5c` je obrnuta kosa crta — dakle upisana su četiri backslasha umjesto potpisa.
Uzrok: pretvorba hex→bajt koristila je `$((16#7a))`, što je **bash/ksh
proširenje**. Gradnju na runneru vrti `dash`, koji to ne zna; aritmetika tiho
padne i ostane goli `\`. Lokalni test je prošao jer msys `sh` podržava `16#`.

Potvrđeno usporedbom pod `dash`-om:

| Oblik | Ishod pod `dash` |
|---|---|
| `$((16#$b))` | `5c5c5c5c` — točno ono što je CI prijavio |
| `$((0x$b))` | `7aea0bf7` — ispravno |

Popravljeno na `0x`, uz dodatnu provjeru **odmah pri upisu** (potpis se pročita
natrag i usporedi), pa se ovakvo što više ne oslanja samo na završnu kontrolu.

Provjereno lokalno na objavljenoj slici prije objave: potpis promijenjen, oba
zapisa `PARTUUID`-a zamijenjena, nula zaostalih starih, MBR potpis `55aa`,
particija 1 zastavica `0x80` 16 MB, particija 2 1024 MB, GRUB i dalje u boot
kodu.

> Vrijedi zapamtiti: annotation koji je dodan da se razlog pada vidi bez
> prijave na GitHub odradio je posao iz prve — bez njega bi se pogađalo.

### Naziv pri raspakiravanju

Korisnik je primijetio da raspakiravanje slike daje datoteku
`personalize.img`. Uzrok: `gzip` u zaglavlje upisuje ime izvorne datoteke, a
radna datoteka pri davanju potpisa zvala se tako. Potvrđeno čitanjem zaglavlja
objavljene v0.36.1 — na pomaku 10 doista piše `personalize.img`.

Popravljeno dvostruko: radna datoteka se sada zove kao i konačna slika, a
pakiranje ide sa `gzip -n` (bez imena i vremena u zaglavlju, uz to je gradnja
ponovljiva). Provjereno: zaglavlje sa spremljenim imenom je 41 bajt, sa `-n`
25 — razlika je točno naziv.

## Konzolni čarobnjak — dvije greške nađene odmah pri upotrebi (v0.37.1)

Korisnik je pri prvoj upotrebi naletio na oboje:

1. **`192.168.50.224/24` je odbijano kao neispravno.** A to je oblik koji se
   najčešće upisuje. Sada se prihvaća i maska se uzme iz prefiksa; maska se
   može upisati i kao `24` i kao `255.255.255.0`.
2. **Provjera nakon primjene bila je prerana.** Čekalo se 4 sekunde, a
   `network restart` traje i do dvadesetak — pa je javljalo „adresa nije
   primijenjena" iako jest. Sada se čeka do 25 s uz točkice, pa ako ne uspije
   slijedi još jedan pokušaj samo za `lan` sučelje, a tek onda ispis stanja
   (što je u konfiguraciji, što na sučelju, koji su portovi u `br-lan` i zadnji
   redci iz dnevnika).

Usput nađeno i treće, prije nego je stiglo do uređaja: pretvorba prefiksa u
masku koristila je `**`, što **nije POSIX** — `dash` i busybox `ash` ga ne
znaju, pa bi maske `/25`–`/31` ispale prazne. Zamijenjeno pomakom `>>`.

Provjereno pod `dash`-om (najbliži busybox `ash`-u):

| Ulaz | Ishod |
|---|---|
| `/8 /16 /22 /24 /25 /26 /30 /31 /32` | sve maske točne |
| `24` i `255.255.255.0` | oboje prihvaćeno |
| `33`, `0`, `abc`, `255.255.255.999` | odbijeno |
| `192.168.50.224/24` | ip `192.168.50.224`, maska `255.255.255.0` |
| `1.2.3.4/33` | odbijen prefiks |

## Gotova slika na stvarnom uređaju (05.08.2026.) — **radi**

Slika v0.37.0 napisana na USB 2.0 stick (14,4 GB) i uređaj IN100 dignut s nje.

| Provjera | Ishod |
|---|---|
| Dizanje s USB-a | **radi** — root `990,7 MB` (slika), ne 220 GB s internog diska. Promjena potpisa diska po slici je odradila svoje |
| Data particija | **`sdb3` 13,4 GB stvorena sama** pri prvom dizanju, iz slobodnog prostora |
| `/opt/saguaro` na njoj | **radi** — `/dev/sdb3 on /opt/saguaro`, sadrži `bin`, `web`, `data`, `etc`, `log`, `backup` |
| Zapis geometrije | `/etc/saguaro-datapart.json` upisan |
| Keep lista | **ispravno izostavlja `/opt/saguaro`** jer je data particija montirana (D-013), a nabraja init skripte, `rc.d` poveznice i zapis o data particiji |
| Saguaro servis | radi; `GET /api/v1/health` → **HTTP 200** |
| Konzolni čarobnjak | `/usr/sbin/saguaro-setup` na mjestu |
| LAN adresa postavljena s konzole | **radi** — `192.168.50.224/24`, zadana ruta preko `192.168.50.1`, uređaj dohvatljiv SSH-om i sučeljem |
| Izrada backupa | **radi** — arhiva 43,6 KB |
| Samoprovjera | **22 prošlo / 1 palo / 14 preskočeno** |

Jedini preostali pad je istinit: kopija ne izlazi s uređaja (nije postavljen ni
poslužitelj ni e-mail). To se rješava postavkama, nije kvar.

### Greška u samoprovjeri nađena istom prilikom

Provjera keep liste javljala je da bi se izgubio `/etc/sysctl.d/99-saguaro.conf`,
a ta datoteka na svježem uređaju **još ne postoji** — nastaje tek kad se
primijeni očvršćivanje. Provjera je tražila datoteku koje nema. Popravljeno:
nepostojeća datoteka se preskače. Nakon popravka 21 prošlo / 2 palo, a nakon
izrade backupa 22 / 1.

## Prijava na uređaju iz slike (v0.38.0) — **propust nađen u upotrebi**

Korisnik se nije mogao prijaviti s `admin` / `Sgs#2026`, iako to piše i u
uputama i u sučelju.

Uzrok: zadanu lozinku postavlja `install.sh` — on u datoteku tokena upiše
doslovno `Sgs#2026`, a Saguaro tu vrijednost uzme kao početnu lozinku admina.
**Slika kroz tu skriptu ne prolazi**, pa je token nasumičan niz — i lozinka je
bila taj niz.

Popravljeno u skripti prvog dizanja: prije prvog pokretanja servisa izvodi se
`saguaro-core -reset-admin 'Sgs#2026'`. API token pritom **ostaje nasumičan**,
što je bolje nego kod instalacije skriptom (ondje su lozinka i token isti niz).

Provjereno na uređaju: nakon postavljanja lozinke prijava vraća sesiju i
`must_change_password: true`, dakle sučelje odmah traži promjenu.

## Instalacija na interni disk — prekinuta na `od` (v0.38.1)

Prva stvarna instalacija stala je nakon kopiranja:

```
/usr/sbin/saguaro-setup: line 255: od: not found
  !! potpis nije 4 bajta — prekidam prije nego išta pokvarim
```

**Busybox u slici nema `od`**, a skripta ga je koristila za čitanje potpisa
diska i za pretvorbu nasumičnih bajtova u hex. Kočnica je odradila svoje —
prekinula je prije nego je upisala prazan potpis — ali je disk ostao
napola instaliran: kopiran, bez vlastitog potpisa i bez usklađenog GRUB-a.

Popravljeno tako da skripta **više uopće ne treba alat za hex**:

| Prije | Sada |
|---|---|
| stari potpis se čitao s diska preko `od` | stari `PARTUUID` se čita **iz samog `grub.cfg`** sa `sed` |
| novi potpis iz `/dev/urandom` preko `od` | novi iz `/proc/sys/kernel/random/uuid` — to je **već tekst** |
| upis bajtova preko `hex2bin` (printf) | isto, ali ulaz više ne ovisi o `od` |

Isto je maknuto i iz init skripte koja nakon nadogradnje vraća data particiju:
provjera ext4 potpisa sada ide `printf` + `cmp` umjesto `hexdump`.

Provjereno pod `dash`-om: UUID → 8 hex znakova → obrnut potpis → **4 bajta**,
stari `PARTUUID` pročitan iz `grub.cfg` i zamijenjen u oba unosa, bez zaostalog
starog zapisa.

### Tri dorade uz to, sve iz stvarne upotrebe

1. **Provjera alata unaprijed.** Instalacija sada odbija krenuti ako nedostaje
   `dd`, `sed`, `cut`, `tr`, `mount`, `umount`, `parted` ili `partx`/`partprobe`
   — umjesto da stane na pola, kad je disk već prepisan.
2. **Napredak pri kopiranju.** Jedan veliki `dd` ne ispisuje ništa dok ne
   završi, pa je 1041 MB s USB 2.0 sticka izgledalo kao da se zaglavilo. Sada
   se kopira u komadima od 32 MB uz ispis postotka, a prekid usred kopiranja
   se javlja i zaustavlja postupak.
3. **Odmontiranje odredišta.** Sustav je sam montirao particiju s odredišnog
   diska (`EXT4-fs (sda3): mounted filesystem`), pa se pisalo ispod montiranog
   datotečnog sustava. Sada se sve s tog diska prvo odmontira.

## Instalacija na disk — uspjela (05.08.2026., v0.38.1)

Uređaj instaliran s USB-a na interni disk i digao se s njega:

| Provjera | Ishod |
|---|---|
| Root particija | **990,7 MB** — diže se s diska, s novom slikom |
| Data particija | **stvorena sama**, 204,8 GB, montirana na `/opt/saguaro` |
| Zapis geometrije | `/etc/saguaro-datapart.json` (disk `sda`, particija 4) |
| Saguaro servis | radi; `GET /api/v1/health` → **HTTP 200** |
| Mreža | `192.168.50.222/24`, zadana ruta preko `.1` — postavljeno kroz konzolni čarobnjak |
| Samoprovjera | **21 prošlo / 2 palo** (oba istinita za svjež uređaj: nema backupa ni kopije izvan uređaja) |

### Neurednost koju je instalacija ostavila

Tablica particija nakon instalacije:

```
sda1     16 MB   boot
sda2   1024 MB   root
sda3   13,4 GB   ← zaostatak, prekopiran s USB-a
sda4    209 GB   data particija
```

Instalacija kopira MBR izvornog medija, pa je s njim došao i **zapis o data
particiji tog medija**. Skripta prvog dizanja je onda vidjela `sda3` kao zadnju
i data particiju napravila tek iza nje — čime je 13,4 GB ostalo neiskorišteno,
a ta particija se zna i sama montirati.

Popravljeno: instalacija nakon kopiranja **briše sve zapise iza root
particije**, pa data particija nastane odmah iza roota i zauzme cijeli ostatak.

## Vraćanje backupa na uređaj s data particijom (05.08.2026., v0.38.2)

Stara konfiguracija (arhiva od 04.08., 82 KB) vraćena na svježe instalirani
uređaj. Usput nađena i popravljena zamka **prije** nego je stigla naštetiti:

`sysupgrade -r` pri vraćanju prepiše cijeli `/etc/config` — uključivo `fstab`.
Arhiva nastala prije nego je data particija postojala nema taj zapis, pa bi se
nakon restarta `/opt/saguaro` prestao montirati: sve vraćeno bi ostalo skriveno
ispod točke montiranja, a uređaj bi se digao s praznom bazom.

Popravak: Saguaro zapamti `fstab.sag_data` **prije** vraćanja i vrati ga ako ga
arhiva nije imala.

| Provjera | Ishod |
|---|---|
| `fstab.sag_data` preživio vraćanje | **radi** — zapis vraćen, particija se montira |
| Restart nakon vraćanja | **radi** — data particija montirana sama, servis se digao |
| Vraćena konfiguracija | mreže (`sag_wg0`, `sag_ovpn`, `sag_wan2`), 4 firewall zone, banIP u jezgri, OpenVPN s cijelim PKI-jem (CA, CRL, ccd, users), 1 klijent |
| Samoprovjera | **41 prošlo / 0 palo** — isto kao stari uređaj prije reinstalacije |

Čišćenje particija odrađeno i ručno na uređaju: mrtvi zapis od 13,4 GB
(prekopiran s USB-a) obrisan, data particija sada jedna od **218 GB** do kraja
diska. Ista logika ugrađena u instalaciju, pa kod budućih instalacija rupa ni
ne nastaje.

## Dijagnostika: aktivne veze i snimanje prometa (v0.39.0)

Provjereno na uređaju **05.08.2026.**:

| Provjera | Ishod |
|---|---|
| Čitanje `/proc/net/nf_conntrack` | **radi** — bez dodatnih paketa; veze sortirane po prometu, zbroj po uređaju, imena iz DHCP leasea |
| Parser na stvarnim redcima | **radi** — jedinični test s pravim zapisima s uređaja (tcp SYN_SENT, udp, ESTABLISHED s [ASSURED]) |
| **Greška nađena na živim podacima** | troznamenkasti timeout (`114`) završavao je kao „stanje" veze — goli brojčani token prošao je heuristiku. Popravljeno (stanje mora sadržavati slovo), test dopunjen upravo takvim retkom |
| Instalacija `tcpdump-mini` na klik | **radi** |
| Snimka 10 s s filterom `port 22` | **radi** — 2160 B, ispravan pcap potpis (`d4c3`), samo se zaustavila nakon isteka, bez zaostalog procesa |
| Preuzimanje i brisanje snimke | **radi** — HTTP 200 / `deleted` |
| Pokušaj izlaska iz direktorija (`..%2F..`) | **odbijen**, HTTP 404 |
| Samoprovjera | 42 prošlo / 1 palo (istinit: vraćena arhiva je starija od SMTP postavki, pa kopija opet ne izlazi s uređaja — treba ponovno unijeti SMTP) |

## Certifikat sučelja (v0.40.0)

Provjereno na uređaju **05.08.2026.**:

| Provjera | Ishod |
|---|---|
| Spremište certifikata s vrućom zamjenom | ugrađeno — `GetCertificate` + nadzor promjene datoteke; self-signed uvijek kao pričuva |
| Krivo DNS ime | **odbijeno**, HTTP 400 |
| Izdavanje bez imena / bez e-maila | **odbijeno**, HTTP 409 |
| Postavljanje imena bez proxyja | **radi** — upisana fw pravila `sag_ac_r80` (DNAT 80→8081) + accept; fw4 prihvatio sve (0 odbijenih) |
| Poslužitelj provjere | **radi** — vraća sadržaj challenge datoteke |
| Cijeli lanac izdavanja (staging) | **radi do očekivanog kraja** — acme konfiguracija upisana (`sag_gui`, staging), `renew` pokrenut, certifikat pošteno javljen kao neizdan (probno ime ne pokazuje na uređaj) |
| Micanje imena | **radi** — počišćena fw pravila i acme zapis (bez toga bi noćni cron zauvijek pokušavao izdati za staro ime — nađeno i popravljeno prije objave), sučelje i dalje radi |
| **Stvarno izdavanje certifikata** | **nije provjerivo iz laba** — uređaj je iza NAT-a bez javnog DNS imena. Kod korisnika s javnom adresom: upiši ime, e-mail i klikni Zatraži; sve ostalo u lancu je dokazano. |

## Korisnici i uloge (v0.41.0)

Provjereno na uređaju **05.08.2026.**, sa stvarnim računima i prijavama:

| Provjera | Ishod |
|---|---|
| Migracija postojeće baze | **radi** — zatečeni `admin` dobio ulogu administratora |
| Validacije (ime, uloga, lozinka) | **odbijeno** s HTTP 400 za svaku |
| Prva prijava novog računa | **radi** — `must_change_password: true`, sve osim promjene lozinke vraća 403 |
| **Operater smije** | `GET /system` 200, `GET /connections` 200, `POST /backup/create` 200 |
| **Operater ne smije** | `/users` 403, `/settings/token` 403, `/openwrt/flash` 403, `/backup/restore` 403, `/system/device-password` 403 |
| **Pregled** | čitanje 200; `POST /backup/create` i `/firewall/apply` 403 |
| Isključen račun | sesija odmah prestaje vrijediti (401), prijava odbijena istom porukom kao za krivu lozinku |
| Zadnji administrator | degradacija, isključivanje i brisanje **odbijeni** s HTTP 409 |
| Samoprovjera | 42 prošlo / 1 palo (nepromijenjeno) |

### Rupa koju je test uhvatio prije objave

Prvi zapis pravila propuštao je `viewer` ulozi **čitanje popisa korisnika** —
provjeravala se samo metoda (`GET`), ne i osjetljivost putanje. Time bi svatko
tko se prijavi vidio sve račune, uloge i vremena zadnjih prijava. Popravljeno:
administratorske putanje su zatvorene i za čitanje, za sve uloge osim
administratora. Jedinični test (`TestPermitted`) pokriva 25 kombinacija uloge,
metode i putanje.

## Dvofaktorska prijava — TOTP (v0.42.0)

Provjereno na uređaju **05.08.2026.**, sa stvarnim računom i stvarnim prijavama.

Ključno u ovom testu: kodovi **nisu** računati Saguarovim kodom, nego zasebnom
implementacijom TOTP-a napisanom za tu priliku (Node, `crypto`), koja prije toga
prolazi službene RFC 6238 vrijednosti. Uređaj je prihvatio kod koji je izračunao
drugi program — to je dokaz da će raditi i prava aplikacija (Google
Authenticator, Microsoft Authenticator, Aegis, 1Password), a ne samo naš kod sam
sa sobom.

| Provjera | Ishod |
|---|---|
| Postavljanje | **radi** — tajna, QR kod (SVG, 89 KB) i `otpauth://` URI s nazivom uređaja |
| Uključivanje krivim kodom | **odbijeno** (401) prije nego se išta spremi |
| Uključivanje kodom iz neovisne implementacije | **radi** — vraćeno 8 pričuvnih kodova |
| Prijava lozinkom kad je 2FA uključena | vraća `totp_required` i međukorak, **bez** sjednice |
| Drugi korak ispravnim kodom | **radi** — dobivena sjednica odmah prolazi na `GET /system` (200) |
| Isti kod dvaput | **odbijeno** — presretnut kod ne vrijedi do kraja svojih 30 sekundi |
| Potrošen međukorak | **odbijeno** (401) |
| Pričuvni kod | **radi**, jednokratan, prihvaća se i bez crtice i velikim slovima; preostalo 8 → 7 → 6 |
| Zapis u dnevnik | prijava pričuvnim kodom zapisana kao upozorenje |
| Operater poništava tuđu 2FA | **odbijeno** — „ovo smije samo administrator" (403) |
| Administrator poništava 2FA | **radi** — prijava se vraća na jedan korak |
| Stupac 2FA u popisu korisnika | prikazuje se (`"totp": true`) |

### Rupa koju je test uhvatio prije objave

Prvi zapis je brisao međukorak pri **svakom** pokušaju, pa je jedan krivo
prepisan kod rušio prijavu i tjerao korisnika da ponovno upisuje ime i lozinku.
Namjera je bila spriječiti pogađanje koda, ali cijena je bila prevelika za nešto
što se prepisuje s ekrana telefona. Sada je dopušteno 5 pokušaja po prijavi, pa
se međukorak briše — pogađanje šesteroznamenkastog koda i dalje nema izgleda.
Pokriveno jediničnim testom (`TestChallengeTries`).

### Što nije provjereno

| Stavka | Zašto |
|---|---|
| Skeniranje QR koda pravom aplikacijom | nema telefona u lancu ispitivanja; `otpauth://` URI je provjeren znak po znak, a aplikacije čitaju upravo njega |
| Ponašanje kad sat na telefonu odluta više od minute | prozor od ±30 s pokriven jediničnim testom, dulji pomak nije simuliran na uređaju |

## Veza ured–ured — site-to-site (v0.43.0)

Provjereno na uređaju **05.08.2026.** Druga poslovnica nije glumljena na
papiru: u zasebnom mrežnom prostoru (`ip netns`) na samom uređaju podignut je
**pravi WireGuard uređaj s vlastitom mrežom** (192.168.60.0/24 iza njega), i to
**isključivo iz konfiguracije koju je Saguaro izdao** za drugu stranu — datoteka
se nije dotjerivala rukom. Time je dokazano i da config koji korisnik preda
drugoj poslovnici stvarno diže vezu.

| Provjera | Ishod |
|---|---|
| Mreža tunela koja se preklapa s LAN-om | **odbijeno** (400), poruka kaže s čime se sudara |
| Mreža tunela koju već koristi OpenVPN (`tun_sag`) | **odbijeno** — vidi rupu ispod |
| Isti UDP port kao udaljeni pristup | **odbijeno** (400) |
| Mreža poslovnice ista kao naša | **odbijeno**, s uputom da jednoj strani treba promijeniti raspon |
| `0.0.0.0/0` kao mreža poslovnice | **odbijeno** — to nije veza ured-ured nego preusmjeravanje interneta |
| Adresa izvan mreže tunela / naša vlastita adresa | **odbijeno** (400) |
| Zapis mreže `192.168.60.5/24` | **normaliziran** u `192.168.60.0/24` |
| Config za drugu stranu | **radi** — drugi ured podignut baš iz njega, handshake odmah |
| **Mi → njihova mreža** (192.168.60.1) | **radi**, 0% gubitka |
| **Oni → naša mreža** (192.168.50.222) | **radi**, 0% gubitka |
| Rute prema njihovoj mreži | postavlja ih sam WireGuard (`route_allowed_ips`) |
| Prosljeđivanje u oba smjera | **radi** — `lan → sagwgs` i `sagwgs → lan` u nftablesu |
| Druga poslovnica → naše upravljanje (SSH, 8443) | **odbijeno** dok je kvačica isključena |
| Kvačica „pristup upravljanju" | **radi** — uključena: sučelje 8443 vraća 200; isključena: veza se ne uspostavlja |
| Ping i DNS prema uređaju | dopušteni, sve ostalo odbijeno (`input_sagwgs`) |
| Javljanje pada veze | **radi** — nakon 5 min tišine zapis „Veza s poslovnicom … je pala", po povratku „…uspostavljena" |
| Jedinični testovi | preklapanje mreža, popis mreža, endpoint, uci popisi — prolaze |

### Dvije rupe koje je test uhvatio prije objave

**1. Sudar s mrežom koju uređaj već koristi.** Provjera preklapanja gledala je
samo mreže upisane u `network` config. Tuneli svoje mreže ondje ne drže, pa je
predložena mreža tunela `10.7.0.0/24` uredno prošla — a na uređaju ju je **već
koristio OpenVPN**. Veza bi radila „na pola" i to bi se tražilo danima. Sada se
provjerava **stvarna tablica ruta jezgre**, pa poruka imenuje i sučelje s kojim
se sudara (`tun_sag`).

**2. Lažna uzbuna kod svakog spremanja postavki.** Podizanje sučelja obriše
vrijeme zadnjeg javljanja, pa je veza deset sekundi nakon spremanja izgledala
kao pala i uređaj je slao „veza je pala", a minutu kasnije „uspostavljena". Uz
Goranovo pravilo da mailom ide samo ono bitno, to je točno ona vrsta poruke
koja se prestane čitati. Sada se pad mjeri **koliko dugo veze nema**, ne
brojem koji restart resetira; uz to se sučelje podiže **samo kad se mreža
stvarno promijenila**, pa kvačica za pristup upravljanju više ne prekida ni
jednu vezu (izmjereno: prije do 30 s prekida, sada bez prekida).

### Što nije provjereno

| Stavka | Zašto |
|---|---|
| Dvije **fizički odvojene** lokacije preko interneta | u labu postoji jedan uređaj; druga strana je pravi WireGuard, ali na istom računalu u odvojenom mrežnom prostoru. Sve što ovisi o putu kroz internet (NAT operatera, MTU na tuđoj vezi) ostaje za prvu stvarnu ugradnju |
| Više od dvije poslovnice odjednom | logika je ista po zapisu, ali isprobana je jedna |
| Promet između dvije poslovnice **preko nas** | namjerno nije dopušten (zona ne prosljeđuje samu u sebe) |

## Mjesečni izvještaj (v0.44.0)

Provjereno na uređaju **05.08.2026.**

| Provjera | Ishod |
|---|---|
| Uzorkovanje svake minute | **radi** — dnevni zapis se puni (mjerenja, WAN, opterećenje, promet) |
| Dostupnost po nadziranom uređaju | **radi** — „glavni router 100 %" iz stvarnih pingova |
| Sastavljanje izvještaja | **radi** — HTML se otvara iz sučelja i vraća kroz API |
| Nepotpun mjesec | **kaže se izrijekom** („zapisi za 1 od 31 dana; postoci se računaju samo iz izmjerenog vremena") |
| Uključivanje bez SMTP-a | **odbijeno** (400) s uputom gdje se SMTP popunjava |
| Dan slanja izvan 1–28 | **odbijeno** (400) |
| Bijeg od HTML-a u porukama | pokriveno jediničnim testom (ime uređaja s `<script>` ne završi kao kod u tuđem sandučiću) |
| Oblik poruke (multipart, kodiran naslov) | pokriveno jediničnim testom |
| Prethodni mjesec na prijelazu godine i 31. u mjesecu | pokriveno jediničnim testom |

### Tri greške koje je test uhvatio prije objave

**1. Prvo mjerenje je u dan upisalo cijeli brojač sučelja.** Brojači sučelja
pokazuju sve od zadnjeg pokretanja uređaja, pa je prvi izvještaj na uređaju u
pogonu pokazao stotine gigabajta „u jednom danu". Sada se prvo viđenje sučelja
samo zapamti kao polazište i ne ulazi u zbroj.

**2. Tablica „promet po uređaju" nikad nije ništa pokazivala — ni u
Monitoringu.** Naredba `nlbw -c csv` unatoč imenu ispisuje razdvojeno
**tabovima**, a kod je razdvajao točkazarezom, pa nije pročitao nijedan redak.
Nađeno pri sastavljanju izvještaja, popravljeno na jednom mjestu za oba modula;
razdjelnik se sada prepoznaje iz zaglavlja.

**3. Dva modula dijelila su iste id-eve u sučelju.** `offsite backup` i `OSPF`
oba su koristili `os-save`, `os-enabled` i `os-result`. Preglednik u tom slučaju
uzima prvi element, pa gumb „Spremi" kod slanja backupa izvan uređaja **nije bio
njegov** — a kod OSPF-a je jedan klik pokretao obje radnje. Popravljeno, a u
`scripts/build.sh` je dodana provjera (`scripts/webcheck.js`) koja dvostruke
id-eve i nepostojeće elemente odsad ruši build.

### Provjereno, a lako se pogriješi

Smjer brojki `rx_bytes` / `tx_bytes` iz nlbwmon-a **nije pogođen nego provjeren
u izvornom kodu** (`client.c`: `rx_bytes → in_bytes`, `nfnetlink.c`: `in_bytes`
su bajtovi koje je primio uređaj u mreži). Pokusi na uređaju nisu mogli
odgovoriti na to pitanje jer nlbwmon ne broji promet prema samom uređaju, nego
samo onaj koji se prosljeđuje — što je i samo po sebi vrijedan nalaz i stoji u
priručniku.

### Što nije provjereno

| Stavka | Zašto |
|---|---|
| Stvarno slanje izvještaja e-mailom | SMTP postavke na uređaju nisu popunjene (vraćena arhiva je starija od njih). Sastavljanje poruke i isporuka kroz `smtpDeliver` isti su put kojim već ide backup e-mailom, koji radi |
| Puni mjesec podataka | mjerenje je počelo danas; brojke za cijeli mjesec bit će prvi put u rujnu |
| Automatsko slanje na zadani dan | traži čekanje do 1. u mjesecu; ručno slanje istog izvještaja radi kroz isti kod |

## Filtriranje sadržaja, DHCP i preslagivanje izbornika (v0.45.0)

Provjereno na uređaju **05.08.2026.**

| Provjera | Ishod |
|---|---|
| Obiteljski DNS (Cloudflare Families) | **radi** — prije: `pornhub.com` → 66.254.114.41; poslije: upit ne prolazi. Obične stranice rade normalno |
| Odgovor filtra izravno s 1.1.1.3 | vraća praznu adresu (`::`) — kroz naš DNSSEC to postaje SERVFAIL, stranica je jednako nedostupna |
| `noresolv` uz odabir | **upisano** — uređaj više ne pita DNS od operatera, pa filtar ne radi samo ponekad |
| Stanje vraćeno nakon testa | **da** — uređaj je vraćen na 1.1.1.1 / 8.8.8.8 kako je i bio |
| DHCP: pravo stanje | **radi** — piše „NE RADI — na ovoj mreži već postoji drugi DHCP poslužitelj" umjesto lažne kvačice |
| DHCP: raspon u pravim adresama | **radi** — 192.168.50.100 – 192.168.50.249 umjesto „100 +150" |
| DHCP: WAN nije nazvan podmrežom | **radi** — „Internet (wan)", uz objašnjenje da se ondje adrese namjerno ne dijele |
| Diagnostics: jednostrani promet | **prepoznaje se** — 102 od 164 veze bez povratnog paketa → „vidi se samo jedan smjer" uz objašnjenje |
| Provjera sučelja u gradnji | **radi** — odmah je uhvatila dva sudara id-eva koje sam napravio (`up-*` prema modulu Updates) |

### Što je test otkrio na uređaju

**DHCP nikad nije ni radio, a sučelje je pokazivalo da radi.** U dnevniku stoji
`found already running DHCP-server on interface 'br-lan' refusing to start`.
OpenWrt je zatekao DHCP na 192.168.50.1 i svoj namjerno nije pokrenuo. Popis
leaseova je zato bio prazan — što je bilo **točno**, ali bez ijedne riječi
objašnjenja. IP adrese koje se vide u System logu su `udhcpc`, tj. ovaj uređaj
**prima** adresu na drugom WAN portu, a ne dijeli je.

**Uređaj vidi samo pola prometa.** Radna stanica ima .222 kao gateway, ali
odgovori joj se vraćaju izravno preko .1 (isti mrežni segment). Zato 62 %
veza stoji kao `[UNREPLIED]`, a stupci *Primljeno* pokazuju nulu. To ujedno
objašnjava i zašto potrošnja po uređaju u nlbwmonu izgleda naopako i zašto se
preuzimanje od 10 MB uopće nije pojavilo u statistici.

### Provjereno kod izvora, a ne pogađano

- **AdGuard obiteljske adrese**: 94.140.14.15 + **94.140.15.16**. Vrlo se često
  prepisuje pogrešan par (94.140.15.15 je obični AdGuard **bez** filtra za
  odrasle) — provjereno u AdGuardovoj dokumentaciji.
- **nlbwmon `rx`/`tx`**: `rx_bytes` = `in_bytes` = ono što je uređaj u mreži
  **primio** (provjereno u izvornom kodu `client.c` i `nfnetlink.c`).
- **Shallalist** koji se po uputama s interneta i danas preporučuje — **ne
  postoji**; domena je danas news-magazin.

### Što nije provjereno

| Stavka | Zašto |
|---|---|
| Preuzimanje dijeljenja adresa od postojećeg routera | traži gašenje DHCP-a na routeru 192.168.50.1, a to je Goranova mreža u pogonu |
| Vlastita lista domena s interneta | polje i zapis su provjereni, samo preuzimanje velike liste nije pokrenuto da se uređaju ne troši memorija bez potrebe |
| Filtriranje s klijentskog računala | radna stanica ne ide na internet kroz ovaj uređaj (vidi gore), pa se s nje ne može izmjeriti |

## Više raspona po mreži i DNS po mreži (v0.46.0)

Provjereno na uređaju **05.08.2026.**, na **izoliranoj testnoj mreži** (most
bez ijednog porta, 10.66.0.1/24) — da se ništa ne dira u mreži u pogonu. Mreža
je nakon testa uklonjena i od nje nema traga (`uci show | grep sagtest` = 0).

| Provjera | Ishod |
|---|---|
| Dva raspona na istoj mreži | **radi** — dnsmasq je dobio oba: `10.66.0.100–150` i `10.66.0.200–230`, oba pod istom oznakom mreže |
| DNS po mreži | **radi** — `dhcp-option=sagtest,6,1.1.1.3,1.0.0.3` (klijenti te mreže dobivaju obiteljski DNS) |
| Domena po mreži | **radi** — `dhcp-option=sagtest,15,gosti.local` |
| Trajanje leasea po mreži | **radi** — 6h |
| Smanjenje broja raspona | **radi** — višak se ukloni, ostaje samo traženi raspon |
| Preklapanje raspona | **odbijeno** — „1. i 2. raspon se preklapaju" |
| Raspon koji sadrži adresu uređaja | **odbijeno** — imenuje adresu (10.66.0.1) |
| Mreže bez raspona | **prikazuju se** s napomenom „nema raspona — klikni Uredi da ga dodaš" |
| Saguaro čita natrag što je upisao | **da** — oba raspona s `running: true` |

### Usput: čemu služi Audit log

Tijekom rada se pokazalo da je **DHCP pool na glavnoj mreži isključen**, a nije
bio kad je posao počeo. Audit log je pokazao točno: promjena u **20:41:08**,
izvor **„admin"** (prijavljena sesija u sučelju), jedan dodani redak — dakle
klik na kvačicu u sučelju, a ne izmjena kroz API token ili izvan Saguara.
Bez tog traga bi se to tražilo po backupima i pogađalo.

### Što nije provjereno

| Stavka | Zašto |
|---|---|
| Klijent koji stvarno dobije adresu iz drugog raspona | testna mreža je most bez portova, pa na njoj nema nijednog uređaja; provjereno je da dnsmasq oba raspona ima u konfiguraciji i da ih drži pod istom mrežom |
| Preuzimanje dijeljenja adresa na glavnoj mreži | traži gašenje DHCP-a na routeru 192.168.50.1 |

---

## Reset lozinke sučelja s konzole (08.08.2026., v0.46.0)

Nova stavka u `saguaro-setup`: **Reset lozinke web sučelja** — postavlja
privremenu lozinku admina (zadano `Sgs#2026`), odjavljuje sve sesije, a
sučelje pri prvoj prijavi traži novu lozinku. Uz to na ekranu za prijavu
stoji „Zaboravljena lozinka?" s uputom (konzola + SSH varijanta).

| Provjera | Ishod |
|---|---|
| Nova stavka u izborniku | **radi** — `saguaro-setup` na uređaju nudi „4) Reset lozinke web sučelja" |
| Tok do potvrde i odustajanje | **radi** — Enter uzima zadanu lozinku, „N" na potvrdi uredno odustane, izbornik se vraća |
| Mehanizam ispod (`-reset-admin` + restart servisa) | **provjeren isti dan drugim putem** — admin lozinka stvarno resetirana (kroz API), prijava vratila `must_change_password: true` |
| Pomoć na ekranu za prijavu | **radi** — poslužena stranica sadrži „Zaboravljena lozinka?" blok |

### Što nije provjereno

| Stavka | Zašto |
|---|---|
| Puni „da" put stavke 4 na uređaju | Goran je isti dan tek postavio svoju novu lozinku — test bi mu je pregazio. Mehanizam je istovjetan onome što je taj dan provjereno kroz API (`resetAdminPassword`), a i skripta prvog dizanja ga koristi od v0.38.0 |

---

## Nadzor UPS-a — NUT (08.08.2026., v0.47.0)

Novi modul UPS (skupina Status): NUT paketi na klik, `upsd` samo na
127.0.0.1, `upsmon` kao master (on gasi uređaj), Saguaro čita `upsc` svakih
15 s i javlja događaje kroz Alerts (vrsta „UPS", zadano isključena).

| Provjera | Ishod |
|---|---|
| Instalacija NUT paketa | **radi** — apk na uređaju (nut-server, nut-upsmon, nut-upsc, driveri usbhid-ups i nutdrv_qx) |
| Upis konfiguracije | **radi** — `nut_server`/`nut_monitor` sag_ zapisi; generirani `/var/etc/nut/ups.conf` sadrži driver, port i `override.battery.charge.low = 30` |
| Pokretanje servisa | **radi** — upsd i upsmon podignuti, uključeni za boot |
| Iskreno stanje bez UPS-a | **radi** — API i sučelje jave „UPS se ne javlja", ništa se ne izmišlja |
| Isključivanje | **radi** — servisi stanu i maknu se iz boota, konfiguracija ostaje |
| Samoprovjera | **radi** — upsd/upsmon provjere prolaze dok je uključen; `pgrep -x` zamijenjen s `pidof` (busybox pgrep -x uspoređuje cijeli redak naredbe) |

### Što nije provjereno

| Stavka | Zašto |
|---|---|
| Očitanje stvarnog UPS-a (status, baterija, autonomija) | na demo uređaju nema UPS-a — treba USB UPS |
| Događaji OL→OB, povratak, LB i e-mail | traže stvarni nestanak struje na UPS-u |
| Uredno gašenje pri praznoj bateriji | radi ga upsmon (standardni NUT mehanizam), ali dokaz traži UPS s praznom baterijom |
