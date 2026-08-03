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
7. **Backup**: odredište izvan uređaja + lozinka za šifriranje (**zapiši je**).
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
