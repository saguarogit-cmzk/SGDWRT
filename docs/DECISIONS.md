# Odluke (ADR — Architecture Decision Record)

Svaka bitna odluka se zapisuje ovdje. Format: ID, odluka, obrazloženje, status.

| ID | Odluka | Obrazloženje | Status |
|----|--------|--------------|--------|
| D-001 | Model: **standalone** (Opcija A) — Saguaro živi na svakom uređaju | Uređaji neće nužno biti isti hardver; nema ovisnosti o centralnom serveru. Inventory shema ipak sadrži `uuid` i `updated_at` po zapisu da kasniji controller-sync bude moguć bez migracije. | Prihvaćeno 2026-07-29 |
| D-002 | Platforma: OpenWrt **25.12.x stable**, x86_64 | Zadnji stable, već na uređaju. Paketni manager: **apk**. Firewall: fw4/nftables. | Prihvaćeno 2026-07-29 |
| D-003 | Core + API u **Go-u**, jedan statični binary `saguaro-core` | Jedan binary bez runtime ovisnosti, cross-compile s Windowsa (`GOOS=linux GOARCH=amd64`), odličan za REST + system pozive. Na 8 GB RAM veličina binarija je nebitna. | Predloženo — čeka potvrdu |
| D-004 | `/opt/saguaro` ostaje **na root particiji** + skriptirani backup | REVIDIRANO 2026-07-29: snimka uređaja pokazala da root (sda2) već zauzima cijeli disk (220 GB). Zasebna particija tražila bi rizično offline smanjivanje roota. Umjesto toga: backup modul mora pokrivati `/opt/saguaro` u cijelosti, a procedura nadogradnje firmwarea je "backup → flash → restore" (x86 sysupgrade prepisuje disk). | Prihvaćeno (rev. 1) |
| D-005 | Saguaro web na **HTTPS :8443**, LuCI ostaje na :443 | Nema kolizije, LuCI ostaje servisni ulaz. Kad Saguaro sazrije, zamjena portova je jedna UCI izmjena. | Prihvaćeno |
| D-006 | Podaci: **SQLite** u `/opt/saguaro/data/saguaro.db` | Bez servera, transakcijski, backup = kopija datoteke. | Prihvaćeno |
| D-007 | Izvor podataka: **isključivo ubus + uci** | `ubus call system board`, `system info`, `network.interface dump`... Stabilna JSON sučelja; nikad parsiranje tekstualnih izlaza. | Prihvaćeno |
| D-008 | Konfiguracija servisa: Saguaro piše **UCI**, ne vlastite daemone | DHCP/DNS = dnsmasq, VPN = wireguard, FW = fw4. Saguaro je upravljački sloj. | Prihvaćeno |
| D-009 | Golden Base = **ImageBuilder** konfiguracija u `image/` | Ponovljiv image (popis paketa + files/ overlay), ne ručno stanje jednog diska. Prvi uređaj smije biti ručno složen; image se izgradi retroaktivno iz njega. | Prihvaćeno |
| D-010 | Autentikacija API-ja **od prve verzije**: token + HTTPS | Naknadno dodavanje autha je uvijek bolno. Self-signed cert za početak. | Prihvaćeno |
