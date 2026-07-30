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
