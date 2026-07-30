# Saguaro Infrastructure — korisnički priručnik

Upravljačka platforma za IN100 (i kompatibilne OpenWrt 25.x x86_64) uređaje.
Ista pomoć dostupna je i u samom sučelju: **Sustav → Pomoć**.

## Temeljna pravila (vrijede u svim modulima)

- **Primijeni**: promjene se najprije spremaju u Saguaro bazu, a na uređaj se
  primjenjuju tek gumbom *Primijeni*. Do tada sučelje pokazuje "⚠ razlika".
- **Backup prije svake izmjene**: uređaj automatski sprema kopiju svake
  konfiguracijske datoteke prije nego je promijeni (vidljivo u Backup modulu).
- **Tuđe se ne dira**: Saguaro upravlja isključivo zapisima koje je sam stvorio
  (`sag_*` oznake). Ručne izmjene i LuCI postavke ostaju netaknute.

## Instalacija na novi uređaj

Na svježem OpenWrt 25.x uređaju (kao root):

```sh
wget -O - https://raw.githubusercontent.com/saguarogit-cmzk/SGDWRT/main/scripts/install.sh | sh
```

Skripta instalira potrebne pakete, preuzme zadnje izdanje s GitHuba i pokrene
servis. Bez objavljenih izdanja: `sh install.sh saguaro-vX.Y.Z-linux-amd64.tar.gz`
(paket se gradi sa `scripts/release.sh`).

Prva prijava na `https://<adresa>:8443/`: korisnik `admin`, lozinka = sadržaj
`/opt/saguaro/etc/token` na uređaju. Odmah je promijeni u **Postavkama**.

---

## Dashboard

Pregled stanja: opterećenje procesora, memorija, disk, vrijeme rada (s malim
grafovima zadnjih sat vremena), stanje fizičkih portova i mrežnih sučelja.
**Internet veza** provjerava tri koraka: izlaz prema mreži (gateway), pretvorbu
imena u adrese (DNS — npr. `google.com` → IP) i stvaran dohvat interneta.

## Mreža

- **LAN adresa**: promjena adrese samog uređaja s validacijama; nakon primjene
  browser se preusmjeri na novu adresu (prijava ostaje ista).
- **WAN sučelja**: veze prema internetu. Protokoli: DHCP klijent, statička
  adresa (podržano **više javnih adresa** na istom WAN-u — sve u polje adresa)
  i PPPoE. Dodatni WAN-ovi (za failover) automatski ulaze u wan firewall zonu.
- **VLAN mreže**: čarobnjak u jednom koraku stvara 802.1q sučelje na odabranom
  portu, podmrežu, DHCP pool i firewall zonu s pristupom:
  *samo internet* (gosti), *internet + LAN* ili *izolirano*. Uređaji u VLAN-u
  spajaju se preko switcha koji tagira taj VLAN prema IN100 portu.

## Multi-WAN

Za uređaje s više internet veza:

- **Failover** — veza s manjim prioritetom je glavna; kad njene nadzorne
  adrese (ping) prestanu odgovarati, promet automatski prelazi na pričuvnu i
  vraća se kad se glavna oporavi.
- **Raspodjela** — promet se dijeli po vezama prema udjelima.
- **Pravila usmjeravanja** — određeni promet (po izvoru, odredištu, portu)
  uvijek ide preko određene veze (npr. računovodstvo preko glavne).

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
- **DNSSEC**: provjera kriptografskih potpisa DNS odgovora — krivotvoreni
  odgovori se odbijaju. Ako nema vlastitih upstream DNS-ova, uređaj uz
  uključivanje postavlja pouzdane javne (ISP routeri često ne prosljeđuju
  DNSSEC podatke pa provjera bez toga ne bi radila).

## Firewall

- **Port forwardi (DNAT)**: usluga iz lokalne mreže postaje dostupna izvana
  (vanjski port ili raspon → interna adresa:port).
- **Pravila prometa**: dopusti (ACCEPT), odbij (REJECT) ili tiho odbaci (DROP)
  promet po zonama, adresama/CIDR-ima i portovima. Prazno odredište znači
  "prema samom uređaju" (npr. dopusti SSH s WAN-a).
- **DMZ**: sav dolazni promet s interneta koji nije uhvaćen forwardima ide na
  jedan interni host. Taj host je potpuno izložen — koristiti s oprezom.
- **1:1 NAT**: javna adresa ↔ interni server u oba smjera (javna adresa mora
  postojati na WAN sučelju).

## Blokade

- **banIP**: promet prema/od poznatih zloćudnih adresa odbacuje se u
  firewallu (nftables setovi — praktički bez opterećenja). Izvori su kurirani
  (FireHOL, IPsum, DShield, Feodo, URLhaus...), a moguća je i blokada cijelih
  zemalja dvoslovnim oznakama. **Iznimke**: IP adrese/CIDR koje se nikad ne
  blokiraju.
- **Blokada domena (adblock-fast)**: reklamne i zloćudne domene blokiraju se
  na DNS razini za sve u mreži; liste se biraju kvačicama (s veličinama).
  **Iznimke**: domene koje se nikad ne blokiraju (npr. vlastita domena) —
  imaju prednost pred svim listama.

## WireGuard i OpenVPN (udaljeni pristup)

Dva ravnopravna VPN-a — WireGuard je brži i moderniji, OpenVPN kompatibilniji
sa starijom opremom. Zajednički model:

- Korisnik se dodaje s **adresom u tunelu**; gumb **Config** daje gotovu
  datoteku (WireGuard conf / .ovpn s ugrađenim certifikatima) za njegovu
  aplikaciju.
- Veze su **split tunnel**: kroz tunel ide samo promet prema lokalnoj mreži,
  na internet korisnik ide vlastitom vezom (za sav promet kroz tunel u
  WireGuardu upiši `0.0.0.0/0` u polje prometa).
- **Pristup po korisniku**: u *ograničenom* načinu korisnik doseže samo ono
  što mu pravila izričito dopuste — segment (zonu), konkretnu adresu, port ili
  raspon. U *punom* načinu svi vide LAN i internet.
- **Ukidanje pristupa**: isključi (ili obriši) korisnika pa *Primijeni* —
  WireGuard peer nestaje s uređaja, OpenVPN klijent gubi pravo spajanja.

## Uređaji (inventar)

Ovaj uređaj upisuje se sam (hardver, serijski broj, verzije — osvježava se pri
svakom startu); uređuju se samo lokacija, klijent i napomene. Susjednu i
klijentsku opremu dodaješ ručno.

## Backup

- **Puni backup** = OpenWrt konfiguracija + Saguaro baza + certifikati i token,
  u jednoj tar.gz arhivi. Čuva se zadnjih 10 na uređaju.
- **Preuzmi** arhive na sigurno mjesto izvan uređaja — to je pravi backup.
- **Vraćanje** prepiše cijelu konfiguraciju i ponovno pokrene uređaj; radi i s
  arhivom drugog uređaja (kloniranje) i nakon reinstalacije firmwarea.
- **Raspored**: automatski dnevni ili tjedni backup u 03:00.

## Ažuriranje

Modul provjerava zadnje izdanje na GitHubu; nadogradnja se pokreće gumbom ili
ručnim učitavanjem paketa. Prije svake nadogradnje automatski se radi puni
backup; nakon zamjene servis se sam ponovno pokreće. Objava izdanja:
`git tag vX.Y.Z && git push --tags` — GitHub Actions sagradi i objavi paket.

## Postavke

- **Lozinka**: promjena traži trenutnu; ostale sesije se odjavljuju.
- **Sesije**: pregled + odjava svih ostalih sesija.
- **API token**: za skripte i integracije (`Authorization: Bearer <token>`);
  regeneracija odmah poništava stari.
- **Syslog**: slanje kopije logova na vanjski poslužitelj (IP, port, UDP/TCP).
