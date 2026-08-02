-- Saguaro Inventory — SQLite shema v1 (ugrađena u binary preko go:embed)
-- Sync-ready: svaki zapis ima uuid i updated_at, pa je buduća
-- sinkronizacija prema centralnom kontroleru moguća bez migracije (D-001).
-- PRAGMA postavke (WAL, foreign_keys) idu kroz DSN, ne ovdje.

CREATE TABLE IF NOT EXISTS devices (
    uuid        TEXT PRIMARY KEY,             -- generira se pri prvom bootu
    hostname    TEXT NOT NULL,
    model       TEXT,                         -- npr. "IN100"
    cpu         TEXT,
    ram_mb      INTEGER,
    disk_gb     INTEGER,
    serial      TEXT,
    firmware    TEXT,                         -- npr. "OpenWrt 25.12.4"
    saguaro_ver TEXT,
    location    TEXT,
    customer    TEXT,
    notes       TEXT,
    is_self     INTEGER NOT NULL DEFAULT 0,   -- 1 = ovaj uređaj; ostali su susjedni/klijentski
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS device_interfaces (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_uuid TEXT NOT NULL REFERENCES devices(uuid) ON DELETE CASCADE,
    name        TEXT NOT NULL,                -- eth0, br-lan...
    role        TEXT,                         -- wan | mgmt | lan | trunk
    mac         TEXT,
    ipv4        TEXT,
    speed_mbps  INTEGER,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (device_uuid, name)
);

-- Klijentski uređaji na mreži (temelj za DHCP static leases i DNS zapise)
CREATE TABLE IF NOT EXISTS hosts (
    uuid        TEXT PRIMARY KEY,
    hostname    TEXT,
    mac         TEXT NOT NULL UNIQUE,
    ipv4        TEXT,                         -- željeni statični lease (NULL = dinamički)
    vlan        INTEGER,
    customer    TEXT,
    notes       TEXT,
    managed     INTEGER NOT NULL DEFAULT 0,   -- 1 = Saguaro generira lease/DNS za njega
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Lokalni DNS zapisi (A/CNAME) koje Saguaro primjenjuje u dnsmasq (sag_* sekcije)
CREATE TABLE IF NOT EXISTS dns_records (
    uuid        TEXT PRIMARY KEY,
    name        TEXT NOT NULL,                -- hostname ili FQDN (malim slovima)
    rtype       TEXT NOT NULL DEFAULT 'A' CHECK (rtype IN ('A','CNAME')),
    value       TEXT NOT NULL,                -- A: IPv4 adresa; CNAME: ciljno ime
    notes       TEXT,
    enabled     INTEGER NOT NULL DEFAULT 1,   -- 0 = ostaje u bazi, ne primjenjuje se
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (name, rtype)
);

-- Split DNS: domena I SVE njene poddomene -> interna adresa servera.
-- Primjenjuje se kao dnsmasq address=/domena/ip, pa lokalni korisnici dolaze
-- izravno na server umjesto "van pa natrag" kroz javnu adresu (bitno za
-- Traefik/Let's Encrypt: ime u certifikatu ostaje isto).
CREATE TABLE IF NOT EXISTS dns_split (
    uuid        TEXT PRIMARY KEY,
    domain      TEXT NOT NULL UNIQUE,
    ip          TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- WireGuard peerovi; privatni ključ postoji samo ako je par generiran na
-- uređaju (omogućuje export klijentskog configa), inače je peer donio svoj javni
CREATE TABLE IF NOT EXISTS wg_peers (
    uuid        TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    public_key  TEXT NOT NULL UNIQUE,
    private_key TEXT,
    tunnel_ip   TEXT NOT NULL UNIQUE,         -- adresa peera u tunelu (bez maske)
    keepalive   INTEGER,                      -- persistent keepalive u s (NULL = isključen)
    enabled     INTEGER NOT NULL DEFAULT 1,
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Pristupna pravila po VPN peeru (vrijede u "ograničenom" načinu pristupa):
-- što smije doseći korisnik spojen kroz tunel — zona/segment, IP/CIDR, port(ovi)
CREATE TABLE IF NOT EXISTS wg_peer_rules (
    uuid        TEXT PRIMARY KEY,
    peer_uuid   TEXT NOT NULL REFERENCES wg_peers(uuid) ON DELETE CASCADE,
    dest_zone   TEXT NOT NULL DEFAULT 'lan',  -- lan | wan | ime zone | '*'
    dest_ip     TEXT,                         -- IP ili CIDR; prazno = cijela zona
    dest_port   TEXT,                         -- port ili raspon; prazno = svi
    proto       TEXT NOT NULL DEFAULT 'tcp udp',
    enabled     INTEGER NOT NULL DEFAULT 1,
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- OpenVPN klijenti: certifikat+ključ generirani na uređaju (za .ovpn export),
-- fiksna adresa u tunelu preko CCD datoteke (ccd-exclusive: bez CCD-a nema spajanja)
CREATE TABLE IF NOT EXISTS ovpn_clients (
    uuid        TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,          -- CN certifikata
    cert_pem    TEXT NOT NULL,
    key_pem     TEXT NOT NULL,
    tunnel_ip   TEXT NOT NULL UNIQUE,
    enabled     INTEGER NOT NULL DEFAULT 1,
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Pristupna pravila po OpenVPN klijentu (isti model kao wg_peer_rules)
-- Opozvani OpenVPN certifikati. Iz ove tablice se generira crl.pem, koji
-- server provjerava pri svakom spajanju — obrisani korisnik se ne može vratiti
-- ni ako netko vrati njegovu CCD datoteku iz backupa.
CREATE TABLE IF NOT EXISTS ovpn_revoked (
    serial      TEXT PRIMARY KEY,               -- serijski broj certifikata (hex)
    name        TEXT NOT NULL,
    not_after   TEXT NOT NULL,                  -- istek certifikata (RFC3339)
    revoked_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS ovpn_client_rules (
    uuid        TEXT PRIMARY KEY,
    client_uuid TEXT NOT NULL REFERENCES ovpn_clients(uuid) ON DELETE CASCADE,
    dest_zone   TEXT NOT NULL DEFAULT 'lan',
    dest_ip     TEXT,
    dest_port   TEXT,
    proto       TEXT NOT NULL DEFAULT 'tcp udp',
    enabled     INTEGER NOT NULL DEFAULT 1,
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Imenovane grupe adresa za firewall pravila (koriste se kao @naziv)
CREATE TABLE IF NOT EXISTS fw_aliases (
    uuid        TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,          -- slug, npr. "serveri"
    ips         TEXT NOT NULL,                 -- IP/CIDR odvojeni razmakom
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Praćeni uređaji (ping nadzor s obavijestima)
CREATE TABLE IF NOT EXISTS nw_monitors (
    uuid        TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    ip          TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    last_ok     INTEGER,                       -- NULL = još nije provjereno
    last_change TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Dnevnik događaja (nadzor, safe mode, novi uređaji)
CREATE TABLE IF NOT EXISTS events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT NOT NULL DEFAULT (datetime('now')),
    level       TEXT NOT NULL DEFAULT 'info',  -- info | warning
    message     TEXT NOT NULL
);

-- MAC adrese viđene u mreži (za alarm o nepoznatom uređaju)
CREATE TABLE IF NOT EXISTS seen_macs (
    mac         TEXT PRIMARY KEY,
    first_seen  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Opće postavke platforme (ključ/vrijednost)
CREATE TABLE IF NOT EXISTS settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Port forwardi (DNAT) — primjenjuju se kao sag_pf_* redirect sekcije u fw4
CREATE TABLE IF NOT EXISTS fw_forwards (
    uuid        TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    proto       TEXT NOT NULL DEFAULT 'tcp udp', -- tcp | udp | tcp udp
    src_zone    TEXT NOT NULL DEFAULT 'wan',
    src_dport   TEXT NOT NULL,                   -- port ili raspon (8000-8010)
    dest_zone   TEXT NOT NULL DEFAULT 'lan',
    dest_ip     TEXT NOT NULL,
    dest_port   TEXT,                            -- prazno = isti kao src_dport
    src_dip     TEXT,                            -- objava na konkretnoj javnoj IP (prazno = sve)
    reflection  INTEGER NOT NULL DEFAULT 1,      -- hairpin NAT (forward radi i iz LAN-a)
    enabled     INTEGER NOT NULL DEFAULT 1,
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Firewall pravila — primjenjuju se kao sag_rl_* rule sekcije u fw4
CREATE TABLE IF NOT EXISTS fw_rules (
    uuid        TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    family      TEXT NOT NULL DEFAULT 'any',     -- any | ipv4 | ipv6
    proto       TEXT NOT NULL DEFAULT 'tcp udp', -- tcp | udp | tcp udp | icmp | all
    src_zone    TEXT NOT NULL DEFAULT 'wan',     -- '*' = bilo koja zona
    src_ip      TEXT,                            -- IP ili CIDR
    dest_zone   TEXT,                            -- prazno = prema samom uređaju (input)
    dest_ip     TEXT,
    dest_port   TEXT,
    target      TEXT NOT NULL DEFAULT 'ACCEPT',  -- ACCEPT | REJECT | DROP
    start_time  TEXT,                            -- HH:MM — pravilo vrijedi od
    stop_time   TEXT,                            -- HH:MM — pravilo vrijedi do
    weekdays    TEXT,                            -- npr. "mon tue fri"; prazno = svi dani
    enabled     INTEGER NOT NULL DEFAULT 1,
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- 1:1 NAT parovi (javna IP <-> interna IP) — sag_n1d_* (DNAT) + sag_n1s_* (SNAT)
CREATE TABLE IF NOT EXISTS fw_nat11 (
    uuid        TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    public_ip   TEXT NOT NULL UNIQUE,
    internal_ip TEXT NOT NULL,
    zone        TEXT NOT NULL DEFAULT 'wan',
    enabled     INTEGER NOT NULL DEFAULT 1,
    notes       TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Korisnički računi za GUI login (pri prvom startu nastaje 'admin'
-- s lozinkom jednakom tadašnjem API tokenu)
CREATE TABLE IF NOT EXISTS users (
    uuid        TEXT PRIMARY KEY,
    username    TEXT NOT NULL UNIQUE,
    pass_hash   TEXT NOT NULL,                -- pbkdf2:<iter>:<salt hex>:<hash hex>
    -- 1 = zadana lozinka s instalacije; do promjene je dopuštena samo promjena
    -- lozinke, jer je zadana lozinka javno poznata i ista na svakom uređaju
    must_change_pw INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Aktivne sesije GUI-ja; čuva se samo SHA-256 sažetak session tokena
CREATE TABLE IF NOT EXISTS sessions (
    token_hash  TEXT PRIMARY KEY,
    user_uuid   TEXT NOT NULL REFERENCES users(uuid) ON DELETE CASCADE,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS schema_version (
    version     INTEGER NOT NULL,
    applied_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO schema_version (version)
    SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_version);
