# Saguaro Infrastructure — što sustav radi i što još može

Analiza stanja na dan **03.08.2026.**, Saguaro Core **v0.24.4**, uređaj IN100
(OpenWrt 25.12.4, x86/64, Intel Atom E3845 · 4 jezgre · 8 GB RAM · 220 GB diska,
4 mrežna porta, bez WiFi radija).

Dokument ima tri dijela:

1. **Što sustav danas radi** — provjereno na uređaju
2. **Odgovor na konkretno pitanje** — više javnih adresa i sve oko NAT-a
3. **Što još možemo dodati** — s procjenom truda i preporukom

Oznake u tablicama:

| Oznaka | Značenje |
|---|---|
| **radi** | provjereno na uređaju, sa stvarnim prometom ili klijentom |
| **postavljeno** | konfiguracija se ispravno zapisuje i servis radi, ali puni scenarij nije praktično isproban (nedostaje vanjski resurs) |
| **nema** | nije implementirano |

---

## 1. Što sustav danas radi

Upravljanje ide kroz vlastito web sučelje (HTTPS, port 8443) i REST API sa
**140 krajnjih točaka**. LuCI ostaje netaknut i radi paralelno. Saguaro dira
**isključivo zapise koje je sam stvorio** (`sag_*`), pa ručne izmjene i
postojeća pravila ostaju netaknuta. Prije svake primjene sam sprema backup
konfiguracije.

### Mreža

| Mogućnost | Stanje | Napomena |
|---|---|---|
| LAN adresa uređaja s validacijom i preusmjeravanjem preglednika | radi | |
| WAN veze: DHCP, statička, PPPoE | radi (DHCP), postavljeno (statička, PPPoE) | |
| **Više javnih adresa na istom WAN portu** | radi | vidi poglavlje 2 |
| VLAN mreže (802.1q) — čarobnjak u jednom koraku | radi | sučelje + podmreža + DHCP + zona + pravila pristupa |
| Cijeli fizički port kao zasebna mreža (DMZ, gosti) | radi | |
| Multi-WAN: failover i raspodjela po udjelima | radi | isprobano stvarnim prekidom veze |
| Pravila usmjeravanja (koji promet ide kojom vezom) | postavljeno | |
| OSPF dinamičko usmjeravanje (bird2) | postavljeno | nema drugog routera za pravu razmjenu ruta |
| QoS — ograničenje brzine po vezi (SQM/CAKE) | postavljeno | |
| DHCP poolovi po mreži, uključivanje/isključivanje | radi | |
| DHCP rezervacije iz inventara | radi | |
| Lokalni DNS zapisi (A, CNAME) | radi | |
| Split DNS (domena + poddomene na interni server) | radi | |
| DNSSEC | radi | |
| Dinamički DNS (DDNS) | postavljeno | nema računa kod pružatelja za pravu provjeru |

### Zaštita

| Mogućnost | Stanje | Napomena |
|---|---|---|
| Zone, pravila prometa (dopusti/odbij/odbaci) | radi | |
| Port forwardi (DNAT) + čarobnjak „Objavi server" | radi | |
| DMZ host | postavljeno | |
| 1:1 NAT (javna adresa ↔ interni server) | postavljeno | traži pravu javnu adresu |
| NAT reflection (hairpin) | radi | |
| Imenovane grupe adresa (aliasi) | radi | |
| banIP — crne liste zloćudnih adresa i blokada po zemlji | radi | |
| adblock-fast — blokada reklamnih i zloćudnih domena | radi | |
| **Detekcija skeniranja portova** | radi | dokazano stvarnim skeniranjem s LAN-a |
| Hardening: 6 mjera koje OpenWrt zadano ne pali | radi | rp_filter, ograničenje pinga, bogon filtar, DNS ne sluša na WAN-u, LuCI na HTTPS, … |
| Ograničenje upravljačkog pristupa (ACL) sa safe modeom | radi | zaključavanje se samo poništi za 2 min |

### VPN

| Mogućnost | Stanje | Napomena |
|---|---|---|
| WireGuard poslužitelj + peerovi, gotov config za klijenta | postavljeno | nema stvarnog klijenta izvana za handshake |
| OpenVPN poslužitelj, vlastiti PKI, `.ovpn` s ugrađenim certifikatima | **radi** | dokazano stvarnim spajanjem klijenta |
| OpenVPN korisničko ime + lozinka uz certifikat | **radi** | ispravna lozinka → spojen, kriva → AUTH_FAILED |
| Opoziv certifikata (CRL) | radi | obrisani korisnik se ne može vratiti ni iz backupa |
| Pristup po korisniku (što smije doseći kroz tunel) | radi | |
| Automatski prijedlog prve slobodne adrese u tunelu | radi | |

### Nadzor i obavijesti

| Mogućnost | Stanje | Napomena |
|---|---|---|
| Dashboard: CPU, RAM, disk, uptime, portovi, sučelja, grafovi zadnjeg sata | radi | |
| Praćenje uređaja pingom | radi | |
| Potrošnja prometa po uređaju (nlbwmon) | radi | |
| **14 vrsta upozorenja e-mailom** | radi (logika), postavljeno (slanje) | WAN pao, javna IP promijenjena, CGNAT, VPN servis, VPN klijent, reboot, promjena konfiguracije, prijava, neuspjele prijave, resursi, backup, istek certifikata, nadzor, nepoznat MAC |
| Trag promjena konfiguracije (tko je što promijenio) | radi | razlikuje izmjenu kroz Saguaro od one izvan njega |
| Sustavski log, trajno spremanje na disk, slanje na vanjski syslog | radi (lokalno), postavljeno (vanjski) | |

### Uređaj i održavanje

| Mogućnost | Stanje | Napomena |
|---|---|---|
| Puni backup (OpenWrt + Saguaro baza + certifikati) | radi | |
| Automatski raspored backupa | radi | |
| **Šifrirano slanje backupa izvan uređaja** | radi | AES-256-GCM, provjeren povratak bajt-u-bajt |
| Vraćanje iz arhive | radi | |
| Nadogradnja Saguara s GitHuba uz automatski backup | radi | |
| Preživljavanje `sysupgrade`-a (keep lista) | radi | |
| Safe mode — rizična promjena se sama poništi ako izgubiš pristup | radi | |
| Samoprovjera uređaja (40 provjera) | radi | `/opt/saguaro/selftest.sh` |
| Inventar opreme | radi | |
| Lozinka uređaja (root/SSH) iz sučelja, API token | radi | |

---

## 2. Odgovor na pitanje: više vanjskih adresa na jednom portu

**Imamo, i radi.** Na jednom WAN portu može stajati više javnih adresa
(`ipaddr` lista u OpenWrt-u) — upišu se sve u polje *Adrese* kod statičke WAN
konfiguracije, odvojene razmakom, svaka s maskom (npr.
`203.0.113.10/29 203.0.113.11/29 203.0.113.12/29`).

Uz to već postoji sve što ide s tim:

| Mogućnost | Stanje | Čemu služi |
|---|---|---|
| **1:1 NAT** | postavljeno | cijela javna adresa preslikana na interni server u oba smjera — server „ima" svoju javnu adresu |
| **Port forward na određenu javnu adresu** | radi | pojedina usluga s točno određene javne adrese ide na točno određeni interni server |
| **DMZ** | postavljeno | sve neuhvaćeno s interneta ide na jedan host |
| **NAT reflection** | radi | i korisnici iznutra dolaze do servera preko javne adrese |
| **Split DNS** | radi | bolje rješenje od reflectiona kad se serveru pristupa imenom |

**Što ovdje još nedostaje:** izbor **izlazne** javne adrese po mreži ili po
klijentu (policy-based SNAT) — npr. „VLAN 20 na internet izlazi kao
203.0.113.11". Vidi prijedlog **A1** ispod.

> Napomena za demonstraciju: ovaj testni uređaj je iza operaterskog NAT-a
> (CGNAT), pa se objava servera prema pravoj javnoj adresi ne može isprobati
> ovdje — treba veza sa stvarnom javnom adresom.

---

## 3. Što još možemo dodati

Sve navedeno je provjereno da **postoji u OpenWrt 25.12.4 repozitoriju za ovaj
uređaj** (11 256 dostupnih paketa) ili se izvodi u samom Saguaru bez novih
paketa. Trud je procijenjen u danima rada.

### A. Mreža i adrese

| # | Mogućnost | Što korisnik dobiva | Kako | Trud | Preporuka |
|---|---|---|---|---|---|
| **A1** | **Izlazna javna adresa po mreži (policy SNAT)** | „Računovodstvo izlazi kao .11, ostali kao .10" — bitno kad pružatelj usluge gleda izvorišnu adresu | nftables `snat to` pravilo po zoni/izvoru; nema novih paketa | 2 dana | **visoka** |
| **A2** | **Statičke rute** | ruta prema mreži iza drugog routera bez OSPF-a | `config route` u `/etc/config/network` | 1 dan | **visoka** (osnovna stvar koja nedostaje) |
| **A3** | **IPv6** | adresiranje, firewall i objava servera preko IPv6 — sve više pružatelja ga daje | `dhcpv6`/`odhcpd` već na uređaju; treba GUI, zone i pravila | 5–8 dana | **srednja** (veliki, ali sve traženiji zahvat) |
| **A4** | **4G/5G pričuvna veza** | uređaj sam prelazi na mobilnu vezu kad optika padne | `modemmanager` ili `uqmi` + USB modem; mwan3 već postoji | 2–3 dana | srednja (traži modem) |
| **A5** | **VRRP / dva uređaja u paru (HA)** | drugi uređaj preuzme cijeli promet ako prvi otkaže | `keepalived` | 4–6 dana | srednja (za kritične lokacije) |
| **A6** | **mDNS preko VLAN-ova** | pisač ili Chromecast iz jedne mreže vidljiv u drugoj, bez spajanja mreža | `mdns-repeater` | 1 dan | srednja |
| **A7** | **IGMP proxy** | operaterska IPTV kroz uređaj | `igmpproxy` | 1 dan | niska (samo ako ima IPTV) |
| **A8** | **DHCP relay** | jedan DHCP server za više mreža | `odhcpd`/dnsmasq relay | 1 dan | niska |
| **A9** | **Wake-on-LAN iz sučelja** | paljenje servera/računala na daljinu | `etherwake`, gumb uz uređaj u inventaru | 0,5 dana | niska, ali se lijepo pokazuje |

### B. Zaštita

| # | Mogućnost | Što korisnik dobiva | Kako | Trud | Preporuka |
|---|---|---|---|---|---|
| **B1** | **Prisilni DNS (blokada vanjskog DNS-a)** | nitko ne može zaobići filtar postavljanjem 8.8.8.8 na svom računalu | preusmjeri port 53 na uređaj + blokiraj 853 i poznate DoH poslužitelje | 1 dan | **visoka** |
| **B2** | **Vremenska pravila** | „gosti na internet samo 08–18", „djeca bez interneta poslije 22" | fw4 podržava vremenska ograničenja izravno | 1–2 dana | **visoka** (vrlo tražena stvar) |
| **B3** | **Ograničenje broja veza po IP-u** | jedno zaraženo računalo ne može zaguši­ti conntrack tablicu | nftables `ct count` | 1 dan | srednja |
| **B4** | **DNS preko TLS-a (DoT)** | upiti prema pružatelju DNS-a šifrirani, ISP ne vidi koje domene tražiš | `stubby` ili `https-dns-proxy` | 1 dan | srednja |
| **B5** | **Blokada po uređaju** | „ovom tabletu samo web, ništa drugo" | pravila po MAC/IP-u iz inventara — dijelom već postoji kroz pravila prometa | 1–2 dana | srednja |
| **B6** | **CrowdSec** | dijeljena reputacija napadača (zajednica šalje adrese) | `crowdsec` + `crowdsec-firewall-bouncer`, ~150 MB RAM-a | 3 dana | niska — banIP već pokriva reputacijske liste |
| **B7** | Suricata / Snort 3 (IDS) | pregled sadržaja prometa | `suricata` nije u repozitoriju za 25.12.4; `snort3` jest | — | **ne preporučam** (dogovoreno) — troši puno, a detekcija skeniranja i banIP pokrivaju najveći dio koristi |

### C. VPN

| # | Mogućnost | Što korisnik dobiva | Kako | Trud | Preporuka |
|---|---|---|---|---|---|
| **C1** | **WireGuard veza ured–ured (site-to-site)** | dvije poslovnice kao jedna mreža, bez klijenata na računalima | postojeći WireGuard + rute i zone | 2–3 dana | **visoka** |
| **C2** | **IPsec (strongSwan)** | veza prema tuđoj opremi koja ne zna WireGuard (Fortinet, Cisco, Sophos) | `strongswan-full` | 5–7 dana | srednja (samo ako druga strana traži) |
| **C3** | OpenConnect poslužitelj (`ocserv`) | klijent koji prolazi kroz restriktivne mreže (radi na TCP/443) | `ocserv` | 3 dana | niska — WireGuard i OpenVPN pokrivaju sve |
| **C4** | L2TP/IPsec, PPTP | — | — | — | **ne** — PPTP je kriptografski razbijen, L2TP problematičan kroz NAT |
| **C5** | Tailscale / ZeroTier | brzo povezivanje bez javne adrese | `tailscale`, `zerotier` | 1 dan | niska za poslovni firewall — promet ovisi o tuđoj kontrolnoj ravnini |

### D. Nadzor, izvještaji i dijagnostika

| # | Mogućnost | Što korisnik dobiva | Kako | Trud | Preporuka |
|---|---|---|---|---|---|
| **D1** | **Pregled aktivnih veza** | „tko trenutno s kim razgovara" — najbrži način da se vidi što troši vezu | `conntrack` + tablica u sučelju | 1 dan | **visoka**, jako se dobro pokazuje |
| **D2** | **Snimanje prometa iz sučelja** | snimka za analizu (`.pcap`) bez SSH-a | `tcpdump-mini`, preuzimanje datoteke | 1–2 dana | **visoka** |
| **D3** | **Mjesečni izvještaj e-mailom** | PDF/HTML sažetak: dostupnost, promet po uređaju, upozorenja, blokade | postojeći podaci + generator | 3 dana | **visoka** za prezentaciju korisniku |
| **D4** | **Povijest prometa po mjesecima** | „koliko smo potrošili u srpnju" | `vnstat2` uz postojeći nlbwmon | 1–2 dana | srednja |
| **D5** | **Izvoz mjerenja u vanjski nadzor** | Zabbix/Grafana/Prometheus kod korisnika | `prometheus-node-exporter-lua` ili vlastiti `/metrics` | 1–2 dana | srednja |
| **D6** | **SNMP** | uređaj vidljiv u postojećem korisnikovom nadzoru | `mini_snmpd` (puni `net-snmp` nije u repozitoriju) | 1–2 dana | srednja |
| **D7** | **Mjerenje brzine veze** | dokaz da veza daje ono što pružatelj naplaćuje | `iperf3` ili speedtest skripta, raspored + graf | 1–2 dana | srednja |
| **D8** | **Nadzor UPS-a** | uredno gašenje pri nestanku struje, upozorenje e-mailom | `nut` | 2 dana | srednja (ako ima UPS) |

### E. Pristup sustavu i sigurnost upravljanja

| # | Mogućnost | Što korisnik dobiva | Kako | Trud | Preporuka |
|---|---|---|---|---|---|
| **E1** | **Više korisnika i uloge** | serviser vidi sve, korisnik samo svoje module; tko je što radio piše u tragu promjena | tablica `users` već postoji, treba uloge i sučelje | 2–3 dana | **visoka** |
| **E2** | **Dvofaktorska prijava (TOTP)** | ukradena lozinka nije dovoljna | u Go-u, bez novih paketa | 1–2 dana | **visoka** |
| **E3** | **Pravi certifikat za sučelje** | nema više upozorenja preglednika | `acme-acmesh` (Let's Encrypt) ili unos vlastitog certifikata | 1–2 dana | **visoka** |
| **E4** | **Prijava kroz Active Directory / LDAP** | korisnici iz domene, bez zasebnih lozinka | LDAP klijent u Go-u | 3–4 dana | srednja |
| **E5** | **Nadogradnja OpenWrt-a iz sučelja** | firmware uz automatski backup i keep listu (koja već radi) | `sysupgrade` + provjera potpisa | 2–3 dana | srednja |
| **E6** | **Zakazane izmjene** | primjena rizične promjene u dogovorenom terminu | raspored + postojeći safe mode | 1–2 dana | niska |

### F. Usluge na samom uređaju

| # | Mogućnost | Što korisnik dobiva | Kako | Trud | Preporuka |
|---|---|---|---|---|---|
| **F1** | **Obrnuti proxy (reverse proxy)** | više servera iza **jedne** javne adrese, razdvojenih po imenu; certifikati na jednom mjestu | `haproxy` ili `nginx` | 3–5 dana | **visoka** ako korisnik ima više web servisa |
| **F2** | Web filtar s pravilima po korisniku | filtriranje po kategorijama sadržaja | `privoxy`/`tinyproxy` (bez HTTPS uvida) | 3 dana | niska — na HTTPS-u daje malo, DNS filtar radi više uz manje |
| **F3** | UPnP | konzole i igre same otvaraju portove | `miniupnpd-nftables` | 1 dan | **s oprezom** — svaki uređaj u mreži smije sam otvoriti port prema internetu |
| **F4** | Dijeljenje datoteka (SMB), medijski poslužitelj | mali NAS na uređaju (ima 220 GB slobodno) | `samba4-server`, `minidlna` | 2 dana | niska — miješa uloge firewalla i servera |

### G. Veći smjerovi razvoja

| # | Mogućnost | Što korisnik dobiva | Trud | Preporuka |
|---|---|---|---|---|
| **G1** | **Središnje upravljanje s više uređaja** | jedna konzola za sve lokacije: verzije, backup, upozorenja, primjena istih pravila | 15–25 dana | **visoka** kad bude više od 3–4 uređaja |
| **G2** | **Predlošci postavki** | nova lokacija podignuta u 10 minuta po provjerenom obrascu | 4–6 dana | visoka |
| **G3** | **Portal za krajnjeg korisnika** | korisnik sam vidi stanje veze i potrošnju, bez prava mijenjanja | 5–8 dana | srednja |

---

## 4. Preporučeni redoslijed

**Prvi krug — brzo, vidljivo, malo rizika (oko 8–10 dana rada)**

1. A2 statičke rute · A1 izlazna javna adresa po mreži
2. B1 prisilni DNS · B2 vremenska pravila
3. D1 pregled aktivnih veza · D2 snimanje prometa
4. E3 pravi certifikat za sučelje

**Drugi krug — ono što korisnik traži kad sustav uđe u ozbiljan pogon (oko 10–12 dana)**

5. E1 više korisnika i uloge · E2 dvofaktorska prijava
6. C1 WireGuard ured–ured
7. D3 mjesečni izvještaj e-mailom
8. E5 nadogradnja OpenWrt-a iz sučelja

**Treći krug — veći zahvati, po potrebi korisnika**

9. A3 IPv6
10. F1 obrnuti proxy
11. A5 HA par uređaja · A4 mobilna pričuvna veza
12. G1 središnje upravljanje s više uređaja

---

## 5. Ograničenja koja treba znati

- **Nema WiFi radija** na IN100 — sve što se tiče bežične mreže (gostinski
  portal, raspored bežične mreže, roaming) traži zasebnu pristupnu točku.
- **Testni uređaj je iza CGNAT-a**, pa se objava servera prema pravoj javnoj
  adresi, DDNS i 1:1 NAT ne mogu do kraja demonstrirati na ovoj lokaciji.
- **Suricata nije u repozitoriju** za OpenWrt 25.12.4 (`snort3` jest), a i
  dogovoreno je da se pregled sadržaja prometa ne uvodi.
- **Resursi nisu ograničenje**: 4 jezgre, 8 GB RAM-a i 220 GB diska su daleko
  iznad onoga što traži bilo koja stavka iz ovog popisa.
