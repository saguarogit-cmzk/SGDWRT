# Saguaro Infrastructure — korisnički priručnik

Upravljačka platforma za IN100 (i kompatibilne OpenWrt 25.x x86_64) uređaje.
Ista pomoć dostupna je i u samom sučelju: **System → Help**.

## Raspored sučelja

Moduli su složeni u šest skupina, po načelu **jedan modul = jedan posao**:

| Skupina | Moduli |
|---|---|
| **Status** | Dashboard · Monitoring · Alerts · Audit log |
| **Network** | Interfaces · Multi-WAN · OSPF · QoS · DHCP · DNS |
| **Firewall** | Firewall rules · Port forwarding / NAT · System access |
| **Filtering** | IP blocklists · DNS filter · Scan detection |
| **VPN** | WireGuard · OpenVPN |
| **System** | Settings · System log · Backup · Inventory · Updates · Help |

**Vodoravna traka na vrhu** bira skupinu, **lijevi stupac** bira modul unutar
nje. Moduli nose ustaljene stručne nazive (DHCP, QoS, Port forwarding, Backup…)
jer se tako zovu i u ostaloj mrežnoj opremi i u uputama na internetu; hrvatsko
objašnjenje stoji ispod naslova svake stranice.

Svaka ploča ima gumb **▾** u naslovnoj traci — klik je sklopi ili rasklopi.
Stanje se pamti u pregledniku, po modulu i ploči, pa ostaje i nakon
osvježavanja stranice.

Na dnu je statusna traka: stanje veze, vrijeme neprekidnog rada, opterećenje,
prijavljeni korisnik i vrijeme zadnjeg osvježavanja podataka.

### Tražilica modula

Gore desno je polje **Traži modul…**. Traži se po nazivu, po **hrvatskim
pojmovima** i po opisu modula, pa nađe i ono što se ne zove onako kako
razmišljaš:

| Upišeš | Nađe |
|---|---|
| `vatrozid` | **Firewall rules** |
| `skenir` | **Scan detection** |
| `reklam` | **DNS filter** — blokada reklamnih domena |
| `lozink` | **Settings** |
| `promjen` | **Audit log** — trag izmjena konfiguracije |
| `kopij` | **Backup** |

Strelicama gore/dolje biraš rezultat, **Enter** otvara modul, **Esc** zatvara
popis. Uz svaki rezultat piše i skupina u kojoj modul živi, pa ga sljedeći put
nađeš i bez tražilice.

## Temeljna pravila (vrijede u svim modulima)

- **Primijeni**: promjene se najprije spremaju u Saguaro bazu, a na uređaj se
  primjenjuju tek gumbom *Primijeni*. Do tada sučelje pokazuje "⚠ razlika".
- **Backup prije svake izmjene**: uređaj automatski sprema kopiju svake
  konfiguracijske datoteke prije nego je promijeni (vidljivo u Backup modulu).
- **Tuđe se ne dira**: Saguaro upravlja isključivo zapisima koje je sam stvorio
  (`sag_*` oznake). Ručne izmjene i LuCI postavke ostaju netaknute.
- **Oznake u tablicama su svugdje iste**, a ispod svake tablice stoji legenda:
  ✔ uključeno (gdje se smije, klik isključuje) · ☐ isključeno ·
  ✎ uredi · 🗑 obriši · ⤓ preuzmi · 👁 prikaži · 🔑 pristup · ⛔ ukloni lozinku.
  Puni naziv radnje piše u oblačiću kad se mišem zadrži nad ikonom.

## Instalacija na novi uređaj

Na svježem OpenWrt 25.x uređaju (kao root):

```sh
wget -O - https://raw.githubusercontent.com/saguarogit-cmzk/SGSWRT/main/scripts/install.sh | sh
```

Skripta instalira potrebne pakete, preuzme zadnje izdanje s GitHuba i pokrene
servis. Bez objavljenih izdanja: `sh install.sh saguaro-vX.Y.Z-linux-amd64.tar.gz`
(paket se gradi sa `scripts/release.sh`).

Prva prijava na `https://<adresa>:8443/`: korisnik `admin`, lozinka
`Sgs#2026`. **Odmah je promijeni** (System → Settings) i regeneriraj API
token — zadane vrijednosti su iste na svakoj novoj instalaciji, pa uređaj s
njima ne smije ostati u produkciji. (Ako je Saguaro postavljen ručno bez
instalacijske skripte, lozinka je sadržaj `/opt/saguaro/etc/token`.)

---

## Dashboard

Pregled stanja: opterećenje procesora, memorija, disk, vrijeme rada (s malim
grafovima zadnjih sat vremena), stanje fizičkih portova i mrežnih sučelja.
**Internet veza** provjerava tri koraka: izlaz prema mreži (gateway), pretvorbu
imena u adrese (DNS — npr. `google.com` → IP) i stvaran dohvat interneta.

## Interfaces — LAN, WAN i VLAN

- **LAN adresa**: promjena adrese samog uređaja s validacijama; nakon primjene
  browser se preusmjeri na novu adresu (prijava ostaje ista).
- **WAN sučelja**: veze prema internetu. Protokoli: DHCP klijent, statička
  adresa (podržano **više javnih adresa** na istom WAN-u — sve u polje adresa)
  i PPPoE. Dodatni WAN-ovi (za failover) automatski ulaze u wan firewall zonu.
- **Dodatne mreže**: čarobnjak u jednom koraku stvara sučelje, podmrežu, DHCP
  pool i firewall zonu s pristupom *samo internet* (gosti/DMZ),
  *internet + LAN* ili *izolirano*. Dvije vrste:
  - **VLAN (tagirano)** — 802.1q na portu; uređaji se spajaju preko switcha
    koji tagira taj VLAN prema portu uređaja. Više mreža dijeli jedan kabel.
  - **Cijeli port** — slobodan fizički port postaje zasebna mreža (DMZ sa
    serverom, WiFi pristupna točka, gostinski port). Port mora biti slobodan;
    ako je član LAN bridgea, prvo ga treba osloboditi.

## Multi-WAN

Za uređaje s više internet veza:

- **Failover** — veza s manjim prioritetom je glavna; kad njene nadzorne
  adrese (ping) prestanu odgovarati, promet automatski prelazi na pričuvnu i
  vraća se kad se glavna oporavi.
- **Raspodjela** — promet se dijeli po vezama prema udjelima.
- **Pravila usmjeravanja** — određeni promet (po izvoru, odredištu, portu)
  uvijek ide preko određene veze (npr. računovodstvo preko glavne).

## OSPF

Automatska razmjena ruta između routera (bird2). Odaberi sučelja koja
sudjeluju ("stub" za mreže s računalima — objavljuju se, ali se na njima ne
traže susjedi), po želji router ID i area. OSPF promet (IP protokol 89)
otvara se samo na zonama odabranih sučelja. Stanje protokola i pronađeni
susjedi prikazuju se u modulu.

## DHCP

- **Poolovi**: po jedan po mreži (lan, VLAN-ovi...); svaki se može isključiti —
  obavezno isključi pool ako u toj mreži adrese već dijeli drugi router
  (inače nastaje sukob, tzv. rogue DHCP).
- **Rezervacije**: uređaj s upisanim MAC-om uvijek dobiva istu adresu. Dodaju
  se ručno ili gumbom *U rezervacije* kod aktivnog leasea. Primjenjuju se
  gumbom *Primijeni rezervacije*.

## DNS

- **Lokalni zapisi**: imena za uređaje u mreži (npr. `nas.lan` umjesto
  192.168.1.50). Tip **A** = ime → IP adresa; **CNAME** = dodatno ime (alias)
  za postojeće ime. Ime bez točke automatski dobiva lokalnu domenu.
- **Split DNS**: domena *i sve njene poddomene* lokalno vode na internu adresu
  servera (dnsmasq `address=/domena/ip`). Rješava scenarij "server je objavljen
  prema internetu, a lokalni korisnici do njega ne mogu": upišeš npr.
  `tvrtka.hr` → `192.168.50.10` i pokriveni su `mail.tvrtka.hr`,
  `app.tvrtka.hr` i ostali. Ime ostaje isto pa Let's Encrypt certifikat
  (Traefik/nginx) i dalje vrijedi, a promet ne ide "van pa natrag".
  Ručno dodani `address=` unosi u dnsmasq konfiguraciji ostaju netaknuti.
- **DNSSEC**: provjera kriptografskih potpisa DNS odgovora — krivotvoreni
  odgovori se odbijaju. Ako nema vlastitih upstream DNS-ova, uređaj uz
  uključivanje postavlja pouzdane javne (ISP routeri često ne prosljeđuju
  DNSSEC podatke pa provjera bez toga ne bi radila).

## Firewall rules i Port forwarding / NAT

- **Port forwardi (DNAT)**: usluga iz lokalne mreže postaje dostupna izvana
  (vanjski port ili raspon → interna adresa:port).
- **Pravila prometa**: dopusti (ACCEPT), odbij (REJECT) ili tiho odbaci (DROP)
  promet po zonama, adresama/CIDR-ima i portovima. Prazno odredište znači
  "prema samom uređaju" (npr. dopusti SSH s WAN-a).
- **DMZ**: sav dolazni promet s interneta koji nije uhvaćen forwardima ide na
  jedan interni host. Taj host je potpuno izložen — koristiti s oprezom.
- **1:1 NAT**: javna adresa ↔ interni server u oba smjera (javna adresa mora
  postojati na WAN sučelju).
- **Čarobnjak "Objavi server"**: interni server (može se izabrati iz
  inventara) + usluge (web, mail, SSH, RDP, vlastiti portovi) + po želji
  konkretna javna adresa → čarobnjak stvori sve potrebne forwarde odjednom.
- **NAT reflection (hairpin)**: opcija uz svaki forward (zadano uključena) —
  serveru preko javne adrese pristupaju i korisnici iznutra. Saguaro izrijekom
  navodi *sve interne zone* (LAN, VLAN-ovi, VPN), jer fw4 zadano pokriva samo
  odredišnu zonu pa korisnici izvan LAN-a inače ostanu bez pristupa.
  Kad se pristupa imenom, uz hairpin postavi i **split DNS** (DNS modul);
  VLAN klijenti trebaju i forwarding pravilo prema mreži servera.

## Filtering — IP blocklists, DNS filter, Scan detection

- **banIP**: promet prema/od poznatih zloćudnih adresa odbacuje se u
  firewallu (nftables setovi — praktički bez opterećenja). Izvori su kurirani
  (FireHOL, IPsum, DShield, Feodo, URLhaus...), a moguća je i blokada cijelih
  zemalja dvoslovnim oznakama. **Iznimke**: IP adrese/CIDR koje se nikad ne
  blokiraju.
- **Blokada domena (adblock-fast)**: reklamne i zloćudne domene blokiraju se
  na DNS razini za sve u mreži; liste se biraju kvačicama (s veličinama).
  **Iznimke**: domene koje se nikad ne blokiraju (npr. vlastita domena) —
  imaju prednost pred svim listama.

- **Detekcija skeniranja portova**: prije napada gotovo uvijek ide izviđanje —
  netko s interneta u nekoliko sekundi kuca na stotine portova. Uređaj takav
  izvor prepozna **po ponašanju** (broju novih veza u sekundi) i privremeno ga
  odbaci. Ne pregledava sadržaj prometa, kao veliki IDS sustavi, pa je trošak
  zanemariv — sve se odvija u firewallu.
  - Zadani prag je namjerno blag: objavljeni web ili mail server kojemu
    posjetitelji dolaze kroz jedan operaterski NAT zna otvoriti puno veza u
    sekundi, a to nije napad.
  - Ako nešto legitimno ipak upadne u zamku (npr. alat za nadzor koji kuca
    prečesto), dodaj ga u **iznimke** i isprazni popis blokiranih.
  - Pravila se pišu u `/etc/nftables.d/`, odakle ih firewall sam uvlači, pa
    preživljavaju restart.

## Audit log (Status)

Uređaj svake minute usporedi svoje postavke s prošlim stanjem i zabilježi što
se promijenilo. Klik na redak pokazuje točnu razliku, redak po redak.

Bitno: hvataju se i promjene napravljene **izvan Saguara** — kroz LuCI ili sa
SSH-a. One se označavaju kao takve jer OpenWrt nema više administratorskih
računa (sve je `root`), pa se ne mogu pripisati osobi. Promjene napravljene
kroz Saguaro nose ime korisnika koji ih je napravio.

## WireGuard i OpenVPN (udaljeni pristup)

Dva ravnopravna VPN-a — WireGuard je brži i moderniji, OpenVPN kompatibilniji
sa starijom opremom. Zajednički model:

- Korisnik se dodaje s **adresom u tunelu**. Uređaj sam ponudi **prvu slobodnu
  adresu** (redom od `.2` naviše) i upiše je u dijalog — dovoljno ju je
  potvrditi, a može se i prepisati. Zauzeta adresa se odbija uz poruku tko je
  već koristi, a mrežna adresa, adresa uređaja u tunelu (`.1`) i broadcast se
  ne mogu dodijeliti.
- Gumb **Config** daje gotovu datoteku (WireGuard conf / .ovpn s ugrađenim
  certifikatima) za korisnikovu aplikaciju.
- Veze su **split tunnel**: kroz tunel ide samo promet prema lokalnoj mreži,
  na internet korisnik ide vlastitom vezom (za sav promet kroz tunel u
  WireGuardu upiši `0.0.0.0/0` u polje prometa).
- **Pristup po korisniku**: u *ograničenom* načinu korisnik doseže samo ono
  što mu pravila izričito dopuste — segment (zonu), konkretnu adresu, port ili
  raspon. U *punom* načinu svi vide LAN i internet.
- **Ukidanje pristupa**: isključi (ili obriši) korisnika pa *Primijeni* —
  WireGuard peer nestaje s uređaja, OpenVPN klijent gubi pravo spajanja.

### Korisničko ime i lozinka uz certifikat (OpenVPN)

Certifikat je *nešto što imaš* — tko dobije `.ovpn` datoteku, spojio se.
Uključivanjem **„Uz certifikat traži i korisničko ime i lozinku"** dodaje se
*nešto što znaš*, pa ukradena datoteka više nije dovoljna.

- **Korisničko ime je naziv klijenta**, lozinka se upisuje u dijalogu klijenta.
- Kad je provjera uključena, **svaki korisnik mora imati lozinku** — oni bez nje
  se ne mogu prijaviti, i u tablici stoji crveno *Nedostaje*.
- Lozinka se čuva **samo kao otisak** (PBKDF2-SHA256, 210 000 iteracija), nikad
  u čitljivom obliku. Otisci idu u `/opt/saguaro/etc/ovpn/users`, datoteku koju
  smije čitati samo `root` i grupa `nogroup` — jer OpenVPN nakon pokretanja radi
  kao `nobody` i mora je pročitati pri prijavi.
- **Ukloni lozinku** u tablici privremeno blokira korisnika bez brisanja
  njegovog certifikata.
- Nakon uključivanja ili isključivanja ove opcije **klijentima treba nova
  `.ovpn` datoteka**, jer se u njoj mijenja redak `auth-user-pass`.

Izvoz iz naredbenog retka (za skriptiranu isporuku):

```sh
saguaro-core -ovpn-export ime-klijenta -out /tmp/klijent.ovpn
```

## Inventory — inventar opreme

Ovaj uređaj upisuje se sam (hardver, serijski broj, verzije — osvježava se pri
svakom startu); uređuju se samo lokacija, klijent i napomene. Susjednu i
klijentsku opremu dodaješ ručno.

## Alerts (Status)

Uređaj sam prati stanje i javlja kad se nešto promijeni. Svaka se vrsta
upozorenja pali zasebno, a ista se poruka ne ponavlja češće od zadanog razmaka
(zadano 30 minuta) — da jedan pokvaren link ne zatrpa sandučić.

Što se prati: pad i povratak internet veze · promjena javne IP adrese · rad iza
tuđeg NAT-a (CGNAT) · pad VPN poslužitelja · spajanje i odspajanje VPN
korisnika · ponovno pokretanje uređaja · promjena konfiguracije · prijave i
veći broj neuspjelih prijava · prelazak praga za procesor, memoriju i disk ·
neuspio backup · skori istek certifikata · nedostupnost praćenog uređaja ·
nepoznat uređaj u mreži.

- **Oznaka uređaja** ide u naslov poruke — korisno kad se nadzire više lokacija.
- **Provjeri sada** pokreće sve provjere odmah, bez čekanja sljedećeg kruga
  (petlja se inače vrti svake minute; javna adresa svakih 5 minuta).
- **CGNAT**: ako uređaj nema vlastitu javnu adresu, objavljeni serveri i
  spajanje na VPN izvana **neće raditi**. To je čest uzrok problema koji je
  inače teško prepoznati, pa uređaj na njega izričito upozori.
- Poruke idu preko SMTP-a uz **obaveznu šifriranu vezu**: port 465 znači TLS
  od početka, na 587 se traži STARTTLS i slanje se odustaje ako ga poslužitelj
  ne nudi — lozinka SMTP računa ne smije putovati u čistom obliku. Koristi
  zaseban račun i lozinku aplikacije (Gmail, Microsoft 365).

## Samoprovjera uređaja

Uz svaku instalaciju dolazi i test koji provjerava **stvarno stanje** uređaja,
ne samo postavke:

```sh
sh /opt/saguaro/selftest.sh              # ništa ne mijenja
sh /opt/saguaro/selftest.sh --disruptive # uz to gasi OpenVPN i gleda vraća li se sam
```

Ispisuje **PROŠLO**, **PALO** (uz uputu kako popraviti) ili **PRESKAČEM** (nije
greška — funkcija nije uključena ili se ne može provjeriti bez vanjskog
resursa). Vrijedi ga pokrenuti nakon svake veće izmjene i nakon nadogradnje.
Detalji i popis onoga što se mora provjeriti ručno: `docs/TESTOVI.md`.

## System access — očvršćivanje (Firewall)

Sitne mjere koje OpenWrt zadano ne uključuje. Svaka je zasebna kvačica jer
neke ovise o tome kako je uređaj spojen, a kvačice pokazuju **stvarno stanje na
uređaju** — ne što je netko namjeravao.

| Mjera | Što radi | Kad je *ne* paliti |
|---|---|---|
| Odbaci krivotvorene izvorišne adrese | Jezgra provjerava dolazi li paket sučeljem kojim bi se odgovorilo pošiljatelju (`rp_filter=2`, labavo) | — postavlja se labavo baš zato da ne razbije više internet veza |
| Ograniči ping s interneta | Uređaj i dalje odgovara na ping, ali najviše 10×/s | ako mjeriš dostupnost alatom koji šalje češće |
| Odbaci privatne adrese s interneta | Paket koji na WAN dolazi s 192.168.x.x ili 10.x.x.x je krivotvoren | **ako je uređaj iza drugog routera** — tada je takav promet normalan i pravilo bi prekinulo vezu (sučelje to samo prepozna i odbije uključiti) |
| DNS ne osluškuje na WAN-u | Servis koji ne sluša prema internetu ne može postati odskočna daska ni ako se firewall jednom pogrešno podesi | — |
| LuCI preusmjeri na HTTPS | Bez toga root lozinka pri prijavi na LuCI putuje mrežom čitljiva | — (preglednik će upozoriti na samopotpisani certifikat, to je očekivano) |
| Ukloni zadana IPsec pravila | OpenWrt zadano propušta IPsec s interneta prema LAN-u; bez instaliranog IPseca to su otvorena vrata koja ništa ne koriste | ako IPsec koristiš (sučelje to prepozna i odbije) |

Sučelje Saguara uz to uvijek traži **TLS 1.2 ili noviji** i šalje zaglavlja koja
pregledniku zabranjuju ugrađivanje stranice u tuđi okvir i učitavanje skripti s
drugih adresa.

## System log (System)

Uređaj zadano drži logove samo u malom spremniku u memoriji (128 kB), pa nakon
svakog ponovnog pokretanja **nestanu** — a s njima i odgovor na pitanje "što se
dogodilo sinoć". Uključivanjem spremanja na disk logovi idu u
`/opt/saguaro/log/system.log` i svaku se noć u 00:05 rotiraju u dnevne
datoteke (`system.log.GGGG-MM-DD.gz`); starije od zadanog broja dana se brišu.
Datoteke se mogu preuzeti iz sučelja. Neovisno o tome, kopija logova može ići i
na vanjski syslog poslužitelj (kartica ispod).

## Backup

- **Puni backup** = OpenWrt konfiguracija + Saguaro baza + certifikati i token,
  u jednoj tar.gz arhivi. Čuva se zadnjih 10 na uređaju.
- **Slanje izvan uređaja**: svaka nova arhiva automatski ide na tvoj server ili
  NAS preko SCP-a. Backup koji leži samo na uređaju nije backup.
  - Prijava ide **SSH ključem** koji uređaj sam napravi; njegov javni dio treba
    dodati na poslužitelju u `~/.ssh/authorized_keys` upisanog korisnika.
  - Arhiva se prije slanja **šifrira** (AES-256-GCM, lozinka se rastegne
    PBKDF2-om) jer sadrži privatne ključeve VPN-a, API token i lozinke.
    **Lozinku zapiši na sigurno — bez nje se arhiva ne može otvoriti.**
  - Otvaranje šifrirane arhive:
    ```sh
    saguaro-core -decrypt-backup arhiva.tar.gz.enc -backup-pass 'lozinka'
    ```
    Radi i na drugom računalu ako se prenese binary.
- **Preuzmi** arhive na sigurno mjesto izvan uređaja — to je pravi backup.
- **Vraćanje** prepiše cijelu konfiguraciju i ponovno pokrene uređaj; radi i s
  arhivom drugog uređaja (kloniranje) i nakon reinstalacije firmwarea.
- **Raspored**: automatski dnevni ili tjedni backup u 03:00.

## Updates — ažuriranje

Modul provjerava zadnje izdanje na GitHubu; nadogradnja se pokreće gumbom ili
ručnim učitavanjem paketa. Prije svake nadogradnje automatski se radi puni
backup; nakon zamjene servis se sam ponovno pokreće. Objava izdanja:
`git tag vX.Y.Z && git push --tags` — GitHub Actions sagradi i objavi paket.

## Settings — postavke

Na uređaju postoje **dvije odvojene lozinke** i lako ih je pomiješati:

| Lozinka | Za što služi | Gdje se mijenja |
|---|---|---|
| **Saguaro** (`admin`) | prijava u ovo sučelje | Postavke → Promjena lozinke |
| **Uređaj** (`root`) | SSH i LuCI | Postavke → Lozinka uređaja |

- **Lozinka Saguara**: promjena traži trenutnu; ostale sesije se odjavljuju.
  Zadana lozinka s instalacije (`Sgs#2026`) ista je na svakom uređaju i javno
  je poznata, pa sučelje pri prvoj prijavi **ne dopušta ništa drugo** dok se
  ne promijeni.
- **Lozinka uređaja**: najmanje 10 znakova; stara se ne traži jer si već
  prijavljen. Prije promjene sprema se kopija `/etc/shadow` u backup.
- **Zaboravljena lozinka Saguara** — vraća se sa SSH-a:
  ```sh
  /etc/init.d/saguaro-core stop
  /opt/saguaro/bin/saguaro-core -reset-admin 'NovaLozinka'
  /etc/init.d/saguaro-core start
  ```
  Sve sesije se odjavljuju, a pri prvoj prijavi sučelje traži novu lozinku —
  jer ova ostaje zapisana u povijesti naredbi.
- **Sesije**: pregled + odjava svih ostalih sesija.
- **API token**: za skripte i integracije (`Authorization: Bearer <token>`);
  regeneracija odmah poništava stari.
- **Syslog**: slanje kopije logova na vanjski poslužitelj (IP, port, UDP/TCP).
