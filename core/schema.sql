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

CREATE TABLE IF NOT EXISTS schema_version (
    version     INTEGER NOT NULL,
    applied_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO schema_version (version)
    SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_version);
