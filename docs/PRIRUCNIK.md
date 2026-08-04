# Saguaro Infrastructure — korisnički priručnik

Upravljačka platforma za IN100 (i kompatibilne OpenWrt 25.x x86_64) uređaje.
Ista pomoć dostupna je i u samom sučelju: **System → Help**.

## Raspored sučelja

Moduli su složeni u sedam skupina, po načelu **jedan modul = jedan posao**:

| Skupina | Moduli |
|---|---|
| **Status** | Dashboard · Monitoring · Alerts · Audit log |
| **Network** | Interfaces · Multi-WAN · OSPF · QoS · DHCP · DNS |
| **Firewall** | Firewall rules · Port forwarding / NAT · System access |
| **Proxy** | Reverse proxy |
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
| `proxy` | **Reverse proxy** — više servisa iza jedne javne adrese |

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
- **Raspored modula je svugdje isti**: gore je tablica s poslom (pravila,
  klijenti, rezervacije, zapisi), a **stanje i gumb za primjenu su u njezinoj
  naslovnoj traci**. Postavke i objašnjenja idu ispod, preko cijele širine.
- **Zone su obojene** kao i drugdje u struci: LAN zelena, WAN crvena, DMZ
  narančasta, GOST plava, VPN ljubičasta. Akcija pravila je isto obojena —
  *DOPUSTI* zeleno, *ODBIJ* narančasto, *ODBACI* crveno.
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

### IPv6

Jedan prekidač u modulu *Interfaces* pali IPv6 **na svim razinama odjednom** —
traženje prefiksa, raspodjelu svim mrežama, objavu adresa uređajima i prikaz
stanja. Nove mreže iz čarobnjaka odmah dobivaju svoj `/64`.

| Način | Što radi |
|---|---|
| **Isključen** | mreže rade samo na IPv4; uređaj ne traži prefiks od pružatelja (zadano) |
| **Automatski** | prefiks se traži od pružatelja (DHCPv6-PD) i sam se dijeli LAN-u i svakom VLAN-u |
| **Ručno** | koristi se vlastiti prefiks (npr. ULA `fd…::/48`) — radi i kad pružatelj ne daje IPv6 |

Uz svaku mrežu piše dodijeljeni prefiks, objavljuje li se (RA), radi li DHCPv6
i koje adrese uređaj stvarno ima.

> **Kod IPv6 nema NAT-a.** Svaki uređaj u mreži dobiva javnu adresu, pa je
> jedina zaštita vatrozid. Zato ostaje **potpuna zabrana dolaznog prometa**, a
> server se objavljuje **izričitim pravilom** u modulu *Firewall rules* s
> obitelji **IPv6** i internom IPv6 adresom kao odredištem — ne port
> forwardom, jer se kod IPv6 adresa ne prevodi nego se promet propušta.

Svako pravilo vatrozida ima izbor obitelji: *IPv4 i IPv6* (zadano), *samo
IPv4* ili *samo IPv6*. Promjenu prefiksa dobivenog od pružatelja uređaj javlja
e-mailom — kod IPv6 se time mijenjaju adrese svih uređaja u mreži.

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
- **Izlazne adrese (SNAT)**: kad uređaj ima **više javnih adresa**, zadano sav
  odlazni promet izlazi s prve. Ovdje se za pojedinu mrežu, podmrežu ili host
  bira druga — npr. „računovodstvo na internet izlazi kao 203.0.113.11". Bitno
  je kad druga strana filtrira po izvorišnoj adresi (banke, državni servisi,
  ugled mail servera).
  - Izvor se bira iz padajućeg popisa lokalnih mreža ili se upiše IP/CIDR/@alias;
    izlazna adresa nudi se iz popisa adresa **stvarno postavljenih** na WAN-u.
    Adresa koje na uređaju nema odbija se s greškom — inače bi promet te mreže
    tiho nestajao.
  - Po želji se pravilo suzi na odredište, port i protokol.
  - **Redoslijed je bitan** (vrijedi prvo pravilo koje odgovara paketu) i mijenja
    se strelicama ▲▼. Pravila stoje **ispred** općeg maskiranja, a **iza** 1:1
    NAT-a, jer je par javna↔interna adresa uži slučaj.
  - Dodatne javne adrese upisuju se u *Network → Interfaces → WAN → Adrese*
    (više adresa na istom portu, odvojenih razmakom).
- **Čarobnjak "Objavi server"**: interni server (može se izabrati iz
  inventara) + usluge (web, mail, SSH, RDP, vlastiti portovi) + po želji
  konkretna javna adresa → čarobnjak stvori sve potrebne forwarde odjednom.
- **NAT reflection (hairpin)**: opcija uz svaki forward (zadano uključena) —
  serveru preko javne adrese pristupaju i korisnici iznutra. Saguaro izrijekom
  navodi *sve interne zone* (LAN, VLAN-ovi, VPN), jer fw4 zadano pokriva samo
  odredišnu zonu pa korisnici izvan LAN-a inače ostanu bez pristupa.
  Kad se pristupa imenom, uz hairpin postavi i **split DNS** (DNS modul);
  VLAN klijenti trebaju i forwarding pravilo prema mreži servera.

## Reverse proxy — više servisa iza jedne javne adrese

Port 443 je samo jedan i može ga uzeti samo jedan interni server, pa port
forward ne pomaže kad treba objaviti `mail.tvrtka.hr`, `crm.tvrtka.hr` i
`kamere.tvrtka.hr` s **jedne** javne adrese. Proxy sluša umjesto njih, gleda
**koje je ime posjetitelj tražio** i proslijedi ga pravom serveru.

| Vrsta | Kako se odlučuje | Certifikat |
|---|---|---|
| **HTTPS — prosljeđivanje** | po imenu iz TLS pozdrava (SNI), veza se ne otvara | ostaje na **internom serveru** |
| **HTTPS — certifikat na uređaju** | uređaj otvara vezu i usmjerava po `Host` zaglavlju | **Let's Encrypt**, uređaj ga sam vodi i obnavlja |
| **HTTP** | po `Host` zaglavlju | nije potreban |

Za HTTPS se koristi **prosljeđivanje bez otvaranja veze**: uređaj pročita samo
ime, a šifriranu vezu proslijedi dalje. Zato mu **ne treba nijedan privatni
ključ** i ne vidi sadržaj prometa. Interni server mora imati valjan certifikat
za to ime. Ako postoji bar jedna HTTPS stranica, ostali promet na portu 80
preusmjerava se na HTTPS.

**Portovi:** proxy ne sjeda na 80 i 443 — njih na uređaju drži LuCI. Umjesto
premještanja upravljanja, proxy sluša na **8080 i 8444**, a vatrozid promet s
interneta s 80 i 443 preusmjeri na njih (`sag_rp_*` zapisi). LuCI i Saguaro
sučelje ostaju netaknuti.

Uz svako ime treba i **javni DNS zapis** prema javnoj adresi uređaja te
**split DNS** (modul DNS), da i korisnici iznutra dolaze na isto ime.

### Certifikati (Let's Encrypt)

Za stranice označene s **certifikat na uređaju** uređaj sam zatraži certifikat
i sam ga obnavlja; interni server tada može ostati na običnom HTTP-u. Upiše se
e-mail za Let's Encrypt račun i pritisne *Zatraži certifikate*.

Da izdavanje uspije, mora vrijediti sve troje:

- **javni DNS zapis** za to ime pokazuje na javnu adresu ovog uređaja,
- **port 80 je dostupan s interneta** (iza operaterskog NAT-a ne radi),
- stranica je **aktivna i primijenjena**.

Provjera ide HTTP-01 postupkom: Let's Encrypt dolazi na port 80 i traži
datoteku u `/.well-known/acme-challenge/`. Vatrozid taj promet već
preusmjerava na proxy, proxy **isključivo tu putanju** šalje malom
poslužitelju unutar Saguara (sluša samo na `127.0.0.1`), a odgovore ondje
ostavlja paket `acme`. Sve ostale putanje do njega ne dolaze.

Izdani certifikati stoje u `/etc/ssl/acme`, a u proxy se povezuju
**poveznicama** — nakon obnove proxy pri ponovnom učitavanju odmah vidi novi
sadržaj. Obnovu vodi paket `acme` noćnim poslom i sam okine ponovno učitavanje
proxyja. Tablica pokazuje za svako ime je li certifikat izdan, do kad vrijedi
i tko ga je izdao; kad ostane manje od 20 dana, stanje se označi narančasto.

Za provjeru postavki postoji **probni poslužitelj** (staging) po stranici:
izdaje certifikat koji preglednici ne priznaju, ali nema stroga ograničenja
broja pokušaja — korisno dok se ne posloži DNS i dostupnost porta 80.

Konfiguracija HAProxyja je generirana (`/etc/haproxy.cfg`) — vidi se gumbom
*Prikaži konfiguraciju*, provjerava se prije zamjene (`haproxy -c`), a stara
se sprema u backup. Bez ijedne aktivne stranice servis se gasi i pravila u
vatrozidu se uklanjaju.

> Instalacija paketa HAProxy na OpenWrt-u sama pokreće servis s **primjerom
> konfiguracije** koji otvara portove 81, 444 i 60000 na svim adresama.
> Saguaro to pri instalaciji odmah zaustavi i zamijeni vlastitom praznom
> konfiguracijom — servis kreće tek kad ima što posluživati.

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

## System access — hardening i ACL (Firewall)

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
- **Slanje na e-mail**: za uređaj koji nema server za kopije, sandučić e-pošte
  je jedina kopija izvan uređaja — a arhiva je mala (obično ispod 100 KB) i
  stane u poruku. Neovisno je o slanju na poslužitelj; može se koristiti oboje
  ili samo jedno.
  - Arhiva se **uvijek** šalje šifrirana, istim postupkom i istom lozinkom kao
    za slanje na poslužitelj. Bez postavljene lozinke se ne šalje ništa —
    nešifrirana kopija ne izlazi s uređaja.
  - **Lozinka nikad ne ide istom porukom.** Stoji u sučelju (Backup → lozinka
    za šifriranje); da putuje uz privitak, šifriranje ne bi značilo ništa.
  - Primatelji se mogu upisati zasebno; prazno polje znači iste kao za
    upozorenja (Nadzor → E-mail).
  - Kvačica *„Pošalji i svaku novu arhivu"* pokriva i noćni raspored.
    Pojedina arhiva se šalje i ikonom ✉ u tablici.
  - Granica privitka je **15 MB** (base64 privitak naraste za trećinu, a
    poslužitelji uglavnom odbijaju poruke preko 25 MB). Veće arhive idu na
    poslužitelj ili ručnim preuzimanjem.
- **Preuzmi** arhive na sigurno mjesto izvan uređaja — to je pravi backup.
- **Vraćanje** prepiše cijelu konfiguraciju i ponovno pokrene uređaj; radi i s
  arhivom drugog uređaja (kloniranje) i nakon reinstalacije firmwarea.
- **Raspored**: automatski dnevni ili tjedni backup u 03:00.

## Updates — ažuriranje

### Saguaro

Modul provjerava zadnje izdanje na GitHubu; nadogradnja se pokreće gumbom ili
ručnim učitavanjem paketa. Prije svake nadogradnje automatski se radi puni
backup; nakon zamjene servis se sam ponovno pokreće. Objava izdanja:
`git tag vX.Y.Z && git push --tags` — GitHub Actions sagradi i objavi paket.

### Disk i korijenska particija

Ovo je jedina veličina koju nadogradnja **tiho promijeni**, pa ima svoju ploču
iznad nadogradnje.

Nadogradnja na ovakvim (x86) uređajima ne upisuje samo sustav nego **cijelu
sliku, zajedno s tablicom particija**. Korijenska particija se time vrati na
veličinu koju slika nosi — zadano oko **104 MB**, bez obzira koliko je disk
velik i kolika je particija bila prije. Sustav se onda kroz par tjedana napuni
do vrha i počne se ponašati nepredvidivo.

Rješenje nije naknadno širenje nego **zadavanje veličine unaprijed**: pri
naručivanju slike upiše se željena veličina korijenske particije. Polje je
već popunjeno preporukom (trostruko od trenutno zauzetog, najmanje 512 MB).
Gornja granica servisa za izgradnju je **1024 MB** i to je za rad sustava
sasvim dovoljno.

Kočnice koje su ugrađene:

- slika se **ne naručuje** ako je tražena particija manja od već zauzetog
  prostora uvećanog za 64 MB rezerve;
- slika se **ne upisuje** ako nosi premalu korijensku particiju; za sliku
  učitanu s računala (veličina se ne zna) traži se izričita potvrda kvačicom;
- veličina prije nadogradnje se zapisuje i nakon dizanja uspoređuje — ako se
  particija smanjila, uređaj **javi e-mailom**;
- `selftest.sh` provjerava koliko je slobodno na korijenskoj particiji.

> **Širenje korijenske particije na uređaju koji radi se ne nudi i ne
> preporučuje.** Službeni `expand-root` postupak radi `resize2fs` preko loop
> uređaja nad particijom koja je u tom trenutku montirana kao korijen za
> pisanje. Na `squashfs` slikama to je bezopasno, ali na **ext4 kombiniranoj
> slici** (kakvu koristimo) to je isti datotečni sustav s dvije strane i
> uništi ga — uređaj se poslije ne digne. Vidi odluku D-012.

Slobodan prostor na disku iza zadnje particije prikazuje se informativno.
Ako ga treba iskoristiti, ide kao **zasebna particija za podatke**, nikad
širenjem korijenske.

### OpenWrt (sustav samog uređaja)

Ploča **OpenWrt** nadograđuje sustav uređaja. Tijek ima tri koraka:

1. **Naruči sliku** — uređaj traži od službenog servisa
   (`sysupgrade.openwrt.org`, isti koji koristi alat `owut`) sliku **s popisom
   paketa ovog uređaja** i **traženom veličinom korijenske particije**. Popis
   paketa je bitan: obična slika s downloads.openwrt.org sadrži samo zadane
   pakete, pa bi nakon nadogradnje nestali mwan3, banIP, OpenVPN, bird2 i
   ostalo. Prva gradnja traje par minuta, kasnije je gotova odmah (servis
   pamti izgrađeno).
2. **Preuzmi na uređaj** — slika se sprema u RAM (`/tmp`) i odmah se provjerava
   **SHA256 otisak**; ako ne odgovara, datoteka se briše i postupak staje.
3. **Nadogradi** — upiše se ime uređaja kao potvrda, napravi se puni backup i
   pokrene `sysupgrade`. Uređaj se ponovno pokreće; sučelje čeka i samo se
   osvježi kad se javi (obično 1–3 minute).

Uređaj bez pristupa internetu: slika se može **učitati s računala**
(`.img.gz`), otisak se izračuna pri prijenosu i provjerava ponovno neposredno
prije upisa.

> **Ovo je jedina radnja u sustavu koja se ne može poništiti na daljinu.**
> Ako slika ne odgovara uređaju, za oporavak treba fizički pristup. Zato ploča
> pokazuje **vrstu pokretanja** (EFI ili BIOS) i **datotečni sustav**
> (ext4/squashfs), a bira se točno odgovarajuća slika — kriva slika je
> najbrži način da uređaj ostane bez sustava.

Što preživi nadogradnju: sve iz `/etc/config`, te Saguaro (binary, baza,
certifikati, VPN PKI) preko popisa `/lib/upgrade/keep.d/saguaro`. Popis paketa
sprema se prije nadogradnje, pa modul nakon dizanja javi ako nešto ipak
nedostaje i ponudi **doinstalaciju jednim klikom**.

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
