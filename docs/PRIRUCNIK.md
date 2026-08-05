# Saguaro Infrastructure — korisnički priručnik

Upravljačka platforma za IN100 (i kompatibilne OpenWrt 25.x x86_64) uređaje.
Ista pomoć dostupna je i u samom sučelju: **System → Help**.

## Raspored sučelja

Moduli su složeni u sedam skupina, po načelu **jedan modul = jedan posao**:

| Skupina | Moduli |
|---|---|
| **Status** | Dashboard · Monitoring · Diagnostics · Alerts · Audit log |
| **Network** | Interfaces · Multi-WAN · Static routes · OSPF · QoS · DHCP · DNS |
| **Firewall** | Firewall rules · Port forwarding / NAT · System access |
| **Proxy** | Reverse proxy |
| **Filtering** | IP blocklists · DNS filter · Scan detection |
| **VPN** | WireGuard · OpenVPN |
| **System** | Settings · Users · System log · Backup · Inventory · Updates · Help |

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

### Preporučeno: gotova slika s USB-a

Uz svako izdanje se objavljuje **gotova slika** —
`saguaro-vX.Y.Z-openwrt-25.12.5-x86-64.img.gz`. U njoj je sve: OpenWrt, svi
paketi, Saguaro program i sučelje, init skripte i postavljanje data particije.
**Uređaj ne treba internet.**

1. Skini sliku sa stranice izdanja na GitHubu (uz nju je i `.sha256`).
2. Napiši je na USB stick — Rufus, način **DD image** (ne ISO).
3. Digni uređaj s USB-a (boot menu, obično F11 ili Del) ili sliku upiši na
   disk uređaja.
4. Otvori `https://192.168.1.1:8443/` i prijavi se: `admin` / `Sgs#2026`.

Pri prvom dizanju uređaj sam napravi **data particiju** od ostatka diska i
preseli `/opt/saguaro` na nju, pa Saguaro podaci od prve minute preživljavaju
buduće nadogradnje OpenWrt-a.

Sučelje te pri prvoj prijavi vodi kroz **prvo postavljanje**: nova lozinka →
ime uređaja, vremenska zona i LAN adresa. Nakon spremanja adrese preglednik se
sam preusmjeri na novu. Sve se kasnije može promijeniti u postavkama.

> Zadana lozinka `Sgs#2026` ista je na svakoj slici i javno je poznata, pa
> sučelje **ne dopušta ništa drugo** dok se ne promijeni. Nakon prijave
> regeneriraj i API token.

### Slika se gradi sama

`image/build.sh` preuzme službeni ImageBuilder (uz provjeru otiska), ubaci
`image/packages.txt` i sve Saguaro datoteke i izgradi sliku s root particijom
od 1024 MB. GitHub Actions to radi pri svakom tagu `vX.Y.Z` i objavljuje
sliku uz izdanje; workflow se može pokrenuti i ručno (*Run workflow*), pa se
slika dobije kao artefakt bez objave izdanja.

### Postojeći uređaj: instalacija skriptom

Za uređaj na kojem OpenWrt već radi (kao root):

```sh
wget -O - https://raw.githubusercontent.com/saguarogit-cmzk/SGSWRT/main/scripts/install.sh | sh
```

Skripta instalira potrebne pakete, preuzme zadnje izdanje s GitHuba i pokrene
servis — **traži internet na samom uređaju**. Bez objavljenih izdanja:
`sh install.sh saguaro-vX.Y.Z-linux-amd64.tar.gz`.

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

## Static routes — statičke rute

Uređaj sam zna put do mreža koje su na njemu i do interneta. Za sve ostalo
treba upisati put: *„za 192.168.100.0/24 idi na 192.168.50.1"*. Tipični
slučajevi: mreža iza drugog rutera, segment na drugoj lokaciji preko VPN-a,
stari dio mreže koji je ostao na zasebnom uređaju.

Za jednu rutu treba: **odredišna mreža** (CIDR, npr. `192.168.100.0/24` — može
i pojedina adresa, sama se pretvori u `/32`), **gateway** (adresa uređaja koji
zna dalje) i **sučelje** kroz koje se do tog gatewaya dolazi. Metrika odlučuje
kad dvije rute vode do istog odredišta — manji broj ima prednost.

Radi i za IPv6 (`route6`); odabir vrste adresa je u istom obrascu.

Provjere koje sprječavaju tihe greške:

- **Zadana ruta (`0.0.0.0/0`) se odbija.** Nju vodi internet veza, a kod više
  veza modul Multi-WAN — statička bi ih tiho zaobišla.
- **Gateway mora biti u mreži odabranog sučelja.** Jezgra takvu rutu inače
  odbije, a u sučelju bi izgledala kao da je primijenjena.
- Odredište se svodi na mrežnu adresu, pa `10.0.0.5/8` ne prođe kao mreža.
- Gateway smije ostati prazan — tada je odredište izravno na tom sučelju
  (on-link); rijetko, ali legitimno.

Izmjene vrijede tek nakon **Primijeni**; do tada ploča stoji na „nije
primijenjeno". Primjena ide kroz **safe mode**: ako kriva ruta presiječe
pristup uređaju i nitko ne potvrdi promjenu, stara se konfiguracija sama vrati
za dvije minute.

Ispod tablice je **tablica usmjeravanja iz jezgre** — stvarno stanje, ne
postavke. Ruta koja je primijenjena mora se ondje pojaviti; ako je nema, nije
prihvaćena. To provjerava i `selftest.sh`.

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
- **Vremenska pravila**: svako pravilo može vrijediti samo u zadanom razdoblju
  dana i na odabrane dane — *„gosti na internet 08–18"*, *„djeci bez interneta
  poslije 22"*. Dani se biraju kvačicama, a stupac **Kada** u tablici pokazuje
  raspored, da se vremenski ograničeno pravilo ne pomiješa s onim koje vrijedi
  uvijek. Vrijeme *do* manje od vremena *od* znači da pravilo prelazi ponoć
  (22:00–06:00). Koristi se **lokalno vrijeme uređaja**, pa vremenska zona mora
  biti postavljena. Ograničenje vrijedi za **nove veze** — već otvoren prijenos
  se ne prekida u trenutku isteka.
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
- **Prisilni DNS**: blokada domena vrijedi **samo dok uređaji koriste DNS ovog
  uređaja**. Dovoljno je da netko na svom računalu upiše `8.8.8.8` i filtra
  više nema. Ovime se sav DNS promet iz odabranih mreža vraća na uređaj, bez
  obzira što je postavljeno na računalu. Tri sloja, jer se DNS zaobilazi na
  tri načina:

  | Sloj | Što radi |
  |---|---|
  | obični DNS (port 53) | preusmjerava se natrag na uređaj |
  | DoT — DNS preko TLS-a (port 853) | odbija se |
  | DoH — DNS preko HTTPS-a (port 443) | odbija se prema poznatim javnim poslužiteljima |

  - **Iznimke**: adrese koje smiju do vlastitog DNS-a (server koji mora
    koristiti DNS svoje domene). Izvedeno imenovanim skupom adresa, pa broj
    iznimki nije ograničen.
  - Primjenjuje se samo na **lokalne mreže**; zone prema internetu se odbijaju,
    jer bi uređaj tada odgovarao na tuđe upite s interneta.
  - **Što ovo ne može**: DoH radi na istom portu kao i običan web, pa se
    blokira samo po adresama poznatih javnih poslužitelja (Google, Cloudflare,
    Quad9, AdGuard, OpenDNS, NextDNS, Mullvad, ControlD). Preglednik koji
    koristi vlastiti DoH preko velike CDN mreže ovime se ne zaustavlja — za to
    bi trebalo pregledavati sadržaj prometa.

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

Uređaj sam prati stanje i **svaki događaj zapisuje u dnevnik** (Status →
Monitoring). To se ne isključuje i ne ovisi ni o kakvim postavkama.

> **Slanje e-mailom je zadano isključeno za sve vrste.** Namjerno: uređaj koji
> javlja svaku promjenu vrlo brzo postane uređaj čije poruke nitko ne čita — a
> onda se prespava i ona koja je bila važna. Uključi samo ono što stvarno želiš
> u sandučiću.

Svaka se vrsta pali zasebno, a ista se poruka ne ponavlja češće od zadanog
razmaka (zadano 30 minuta) — da jedan pokvaren link ne zatrpa sandučić.

Jedino što e-mailom odlazi neovisno o ovom popisu je **sigurnosna kopija**,
prema postavci u modulu Backup (zadano **jednom tjedno**).

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
  - **Učestalost**: uz svaki backup, jednom dnevno, tjedno ili mjesečno.
    Uređaj radi arhivu svaku noć, ali ne mora svaku noć slati poruku — inače
    se gomilaju i prestanu se gledati. Zadano je **jednom tjedno**. Razmaci su
    namjerno malo kraći od punog razdoblja (tjedno = 6 dana i 20 sati) jer
    backup ide uvijek u isti sat, pa razlika od par minuta ne smije preskočiti
    cijeli tjedan.
  - Gumb *Pošalji zadnju arhivu odmah* i ikona ✉ u tablici **ne pitaju za
    učestalost** — ako čovjek klikne, poruka ide.
  - Granica privitka je **15 MB** (base64 privitak naraste za trećinu, a
    poslužitelji uglavnom odbijaju poruke preko 25 MB). Veće arhive idu na
    poslužitelj ili ručnim preuzimanjem.
- **Preuzmi** arhive na sigurno mjesto izvan uređaja — to je pravi backup.
- **Vraćanje** prepiše cijelu konfiguraciju i ponovno pokrene uređaj; radi i s
  arhivom drugog uređaja (kloniranje) i nakon reinstalacije firmwarea.
- **Raspored**: automatski dnevni ili tjedni backup u 03:00.

## Updates — ažuriranje

Ovdje se nadograđuju **dvije potpuno različite stvari**. Miješaju se lako, pa
ih modul drži odvojeno i tako su opisane i ovdje:

| | Što se nadograđuje | Odakle | Što dira | Restart |
|---|---|---|---|---|
| **1** | **Saguaro** — ovo sučelje i API | GitHub `SGSWRT` ili učitani paket | samo `/opt/saguaro` | samo servis, ~2 s |
| **2** | **OpenWrt** — sustav samog uređaja | službeni build servis ili učitana slika | cijeli sustav uređaja **i tablicu particija diska** | cijeli uređaj, 1–3 min |

Saguaro se nadograđuje često i bezopasno. OpenWrt rijetko, i **to je jedina
radnja koja se ne može poništiti na daljinu**.

### 1. Saguaro (s GitHuba)

Modul provjerava zadnje izdanje na GitHubu; nadogradnja se pokreće gumbom ili
ručnim učitavanjem paketa (za uređaj bez pristupa internetu). Prije svake
nadogradnje automatski se radi puni backup; nakon zamjene servis se sam
ponovno pokreće. Konfiguracija, baza i certifikati se ne diraju.

Objava izdanja: `git tag vX.Y.Z && git push --tags` — GitHub Actions sagradi i
objavi paket.

### Disk i root particija (tiče se samo nadogradnje OpenWrt-a)

Ovo je jedina veličina koju nadogradnja **tiho promijeni**, pa ima svoju ploču
iznad nje.

Nadogradnja na ovakvim (x86) uređajima ne upisuje samo sustav nego **cijelu
sliku, zajedno s tablicom particija**. Root particija se time vrati na
veličinu koju slika nosi — zadano oko **104 MB**, bez obzira koliko je disk
velik i kolika je particija bila prije. Sustav se onda kroz par tjedana napuni
do vrha i počne se ponašati nepredvidivo.

Rješenje nije naknadno širenje nego **zadavanje veličine unaprijed**: slika se
od build servisa naručuje s traženom veličinom root particije. **Ništa se ne
upisuje ni računa** — Saguaro traži najveće što servis daje (**1024 MB**), a to
je za rad sustava i više nego dovoljno (sustav troši oko 75 MB).

Kočnice koje su ugrađene:

- slika se **ne naručuje** ako je tražena particija manja od već zauzetog
  prostora uvećanog za 64 MB rezerve;
- slika se **ne upisuje** ako nosi premalu root particiju; za sliku
  učitanu s računala (veličina se ne zna) traži se izričita potvrda kvačicom;
- veličina prije nadogradnje se zapisuje i nakon dizanja uspoređuje — ako se
  particija smanjila, uređaj **javi e-mailom**;
- `selftest.sh` provjerava koliko je slobodno na root particiji.

> **Širenje root particije na uređaju koji radi se ne nudi i ne
> preporučuje.** Službeni `expand-root` postupak radi `resize2fs` preko loop
> uređaja nad particijom koja je u tom trenutku montirana kao root za
> pisanje. Na `squashfs` slikama to je bezopasno, ali na **ext4 kombiniranoj
> slici** (kakvu koristimo) to je isti datotečni sustav s dvije strane i
> uništi ga — uređaj se poslije ne digne. Vidi odluku D-012.

### Data particija

Slobodan prostor na disku ne ide u root particiju nego u **zasebnu data
particiju** — to je podjela koju koriste i uređaji s ozbiljnim firmwareom:

| Particija | Što nosi | Pri nadogradnji OpenWrt-a |
|---|---|---|
| **root, 1 GB** | OpenWrt i sam Saguaro program | **prepisuje se** — tako i treba |
| **data, ostatak diska** | `/opt/saguaro`: baza, backupi, logovi, VPN ključevi, certifikati | **ne dira se** — disk image je velik 1 GB i ne dopire do nje |

Poslije nadogradnje treba vratiti samo **zapis** o toj particiji u tablicu
(nekoliko bajtova), a ne dirati podatke. To radi skripta pri dizanju uređaja,
prije montiranja, i to **tek nakon što provjeri da na zapisanom mjestu stvarno
postoji ext4 superblok**. Ako potpisa nema, tablica se ne dira — radije nema
data particije nego pogrešan zapis preko tuđih podataka.

Dok je data particija u pogonu, keep lista više ne nabraja `/opt/saguaro`;
podaci više ne ovise o tome je li netko zapamtio dodati putanju u popis.

**Zahvat traži jednu nadogradnju OpenWrt-a.** Root particija na postojećem
uređaju obično zauzima cijeli disk, pa za data particiju nema mjesta.
Oslobađanje bi tražilo smanjivanje ext4 na živom uređaju — a to je postupak
koji je jednom već oborio uređaj (D-012). Umjesto toga posao radi sama
nadogradnja: nova slika nosi tablicu s rootom od 1024 MB i time oslobodi
ostatak diska. Redoslijed je:

1. **Backup** → puna kopija, pošalji je i na e-mail.
2. **Updates → 2. OpenWrt** → naruči sliku, preuzmi, nadogradi (uređaj se diže
   s rootom od 1024 MB).
3. **Updates → Data particija** → gumb *Stvori data particiju i preseli
   podatke* (traži ime uređaja kao potvrdu; prije zahvata se radi još jedan
   puni backup).

Od tada svaka sljedeća nadogradnja OpenWrt-a ostavlja podatke netaknutima.

### 2. OpenWrt (sustav samog uređaja)

Ploča **OpenWrt** nadograđuje sustav uređaja. Tijek ima tri koraka:

1. **Naruči sliku** — uređaj traži od službenog servisa
   (`sysupgrade.openwrt.org`, isti koji koristi alat `owut`) sliku **s popisom
   paketa ovog uređaja** i **traženom veličinom root particije**. Popis
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

## Postavljanje s konzole — `saguaro-setup`

Uređaj iz slike dolazi na `192.168.1.1`, a to gotovo nikad nije adresa mreže u
koju se stavlja. Dok se do sučelja ne može, sve bi se moralo tipkati po
konzoli. Zato na konzoli postoji izbornik — pokreće se naredbom:

```sh
saguaro-setup
```

Podsjetnik na njega ispisuje se pri svakoj prijavi na konzolu, zajedno s
adresom sučelja.

| Stavka | Što radi |
|---|---|
| **Pregled stanja** | ime uređaja, verzije, LAN i WAN adresa, zadana ruta, root i data particija, adresa sučelja |
| **Postavi LAN adresu** | IP, maska, gateway i DNS uz provjeru svake vrijednosti; Enter zadržava postojeće. Nakon spremanja radi `network restart` (ne `reload` — kod promjene adrese zna ostati na pola) i **provjeri je li adresa stvarno primijenjena** |
| **Instaliraj na interni disk** | prepiše sustav s medija s kojeg se diglo na odabrani disk |
| Ponovno pokreni / Ugasi | uredno gašenje s konzole |

### Instalacija na interni disk

Namijenjena je uobičajenom slučaju: uređaj se digne s USB-a, pa se sustav
prebaci na njegov disk.

Kočnice, jer je radnja nepovratna:

- disk s kojeg sustav radi **ne nudi se** kao odredište;
- prije upisa se traži da se upiše točno `obrisi <disk>` — ne samo „da";
- odredište koje je manje od onoga što treba upisati se odbija.

Nakon kopiranja odredišni disk dobiva **vlastiti potpis diska** i njegov
`grub.cfg` se uskladi s njim. Bez toga bi imao isti potpis kao medij s kojeg
je kopiran, pa jezgra kod `root=PARTUUID=…` ne bi znala koji je koji — i
sustav bi se znao dići s krivog diska (vidi D-015).

Odredištu se vraća i skripta prvog dizanja, pa pri prvom pokretanju s diska
sam napravi **data particiju** od ostatka diska. Zapis o data particiji medija
s kojeg je kopirano se briše — odnosi se na taj medij, ne na novi disk.

## Diagnostics (Status) — aktivne veze i snimanje prometa

### Tko troši vezu

Čita se conntrack tablica same jezgre (`/proc/net/nf_conntrack`) — bez
dodatnih paketa. Gornja tablica zbraja po **uređaju u mreži** (ime iz DHCP
leasea, broj veza, poslano/primljeno), najveći promet na vrhu. Donja tablica
su pojedinačne veze s filterom (adresa, port ili ime); prikazuje se prvih 200.

Brojke su promet **trenutno otvorenih veza**, ne povijest — za mjesečnu
potrošnju služi Monitoring (nlbwmon).

### Snimanje prometa (.pcap)

Snimka paketa za analizu u Wiresharku, bez SSH-a. Alat (`tcpdump-mini`) se
instalira jednim klikom, kao i haproxy za obrnuti proxy.

- Bira se **sučelje**, **trajanje** i po želji filter (`host 192.168.50.10`,
  `port 53`…).
- Snimaju se **zaglavlja paketa** (prvih 96 bajtova), ne cijeli sadržaj — za
  analizu je dovoljno, snimka ostaje mala i ne zadire u sadržaj komunikacije.
- Granice: **10 minuta** odnosno **100 MB** po snimci; snimanje se tada samo
  zaustavi. Granice čuva Saguaro nadzor, ne tcpdump — pokriva i slučaj da
  proces ostane visjeti. Zaboravljena snimka ne može puniti disk danima.
- Snimke leže na **data particiji** i preuzimaju se odnosno brišu iz tablice.

## Certifikat sučelja (Settings)

Sučelje zadano radi sa self-signed certifikatom — veza je šifrirana, ali
preglednik upozorava. S pravim certifikatom (Let's Encrypt) upozorenja
nestaju, a korisnik se odvikava od klikanja „prihvati rizik".

**Preduvjeti:** javna adresa (ne CGNAT), DNS ime koje pokazuje na nju i
port 80 dostupan izvana za HTTP-01 provjeru. Ako obrnuti proxy radi, provjera
ide kroz njega; inače Saguaro sam upiše preusmjerenje porta 80 na svoj
poslužitelj provjere (koji poslužuje isključivo putanju
`/.well-known/acme-challenge/`). Ta se dva puta **ne preklapaju** — pri
uključivanju proxyja izravno preusmjerenje se samo makne, i obrnuto.

Postupak: upiši DNS ime i e-mail (Let's Encrypt račun), po želji probni
poslužitelj (staging) za isprobavanje, pa **Zatraži certifikat**. Certifikat:

- vrijedi za :8443 **odmah, bez restarta** — poslužitelj ga uzima iz spremišta
  koje se samo osvježi kad se datoteka promijeni;
- **obnavlja se sam** (noćni cron paketa acme) i zamjena opet prolazi bez
  prekida;
- ako ga nema ili je neispravan, sučelje **uvijek** pada natrag na
  self-signed — nikad ne ostaje bez TLS-a.

Micanjem DNS imena sve se počisti: firewall preusmjerenje, acme zapis (da ga
noćni cron ne pokušava obnavljati) i povratak na self-signed.

## Users (System) — korisnici i uloge

Svaka osoba svoj račun: promjene se u dnevniku pripisuju **osobi**, a ne svima
pod istim imenom. Tri uloge, namjerno malo — više njih nitko ne bi ispravno
postavio:

| Uloga | Što smije |
|---|---|
| **Administrator** | sve, uključivo korisnike, API token i nepovratne zahvate |
| **Operater** | svakodnevni rad: mreža, firewall, VPN, DHCP, backup, dijagnostika |
| **Pregled** | samo gledanje (GET), ništa se ne mijenja |

Operateru i pregledu zatvoreno je ovo: **upravljanje korisnicima**, **API
token** (pročitan je jednako opasan kao promijenjen), **lozinka uređaja**,
**upis firmwarea**, **dijeljenje diska** i **vraćanje backupa**. Popis je
namjerno kratak i drži se dvije vrste opasnosti: preuzimanje potpune kontrole i
nepovratni zahvati.

- **Nova lozinka koju postavi administrator je privremena** — korisnik je mora
  promijeniti pri prvoj prijavi, a sve njegove sesije se odmah zatvaraju. Inače
  bi lozinku znalo dvoje ljudi.
- **Isključen račun** ne može ni ući ni nastaviti raditi s otvorenom sesijom.
  Prijava mu javlja istu poruku kao za krivu lozinku — izvana se ne razaznaje
  postoji li račun.
- **Zadnji administrator** se ne može obrisati, isključiti ni degradirati, a
  **vlastitom računu** ne možeš oduzeti prava — inače bi se čovjek zaključao
  van, i to najčešće ne primijeti dok ne bude kasno.
- Korisničko ime se poslije **ne mijenja** — dnevnik bi izgubio trag.
- Moduli koje uloga ne smije otvoriti **skrivaju se iz izbornika**, da se ne
  kuca u zabranjena vrata. Uloga piše i u statusnoj traci.

> **API token zaobilazi uloge** — on je strojni pristup s punim pravima. Zato
> ga smije vidjeti i mijenjati samo administrator.
