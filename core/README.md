# Saguaro Core

Jedan Go binary (`saguaro-core`) koji na uređaju radi sve:

- servira REST API v1 (nacrt endpointa: `docs/ARCHITECTURE.md`)
- servira statički web (SPA iz `/opt/saguaro/web`) na HTTPS :8443
- čita stanje sustava kroz `ubus` (exec `ubus call ... ` → JSON decode)
- drži SQLite bazu (`/opt/saguaro/data/saguaro.db`)
- token autentikacija (Bearer), self-signed TLS za početak

## Build (s Windows radne stanice)

```
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0        # čisti statični binary; SQLite preko modernc.org/sqlite (bez CGO)
go build -o dist/saguaro-core ./core
```

Deploy: `sh scripts/deploy.sh root@192.168.100.1`

## Init skripta

Na uređaju `/etc/init.d/saguaro-core` (procd) — pokreće binary, restart on crash.
Predložak ide u `image/files/etc/init.d/` kad se složi Golden Base.
