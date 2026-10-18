# GoCasa

A self-hosted family calendar dashboard: one static Go binary, one SQLite file, server-rendered UI. See [the spec](docs/skylite-go-spec.md) and [the glossary](GLOSSARY.md).

## Run it with Docker Compose

```sh
cp .env.example .env
# fill in APP_ENCRYPTION_KEY and SESSION_SECRET: openssl rand -base64 32
docker compose up -d
```

Open `http://<host>:8080`. `GET /healthz` returns `200 ok` when the app is up and the database answers.

Data lives in the `gocasa-data` volume at `/data` (`app.db`, and later `backups/`, `photo-cache/`, `photos/`). To use a bind mount instead, make the host directory writable by UID 65532, which the container runs as: `sudo chown 65532:65532 ./data`.

## Configuration

Config comes from environment variables only. There is no config file.

| Variable | Default | Meaning |
|---|---|---|
| `ADDR` | `:8080` | Listen address. GoCasa serves plain HTTP only |
| `BASE_URL` | (required) | How clients reach GoCasa, e.g. `https://casa.example.com`. Drives Secure cookies, the Google redirect and absolute links |
| `DATA_DIR` | `/data` | Holds `app.db` and its siblings |
| `TZ` | `UTC` | The Household's default IANA zone, e.g. `America/Los_Angeles`. Events keep their own zones. Zone data is built into the binary |
| `APP_ENCRYPTION_KEY` | (required) | 32 random bytes, base64. Encrypts OAuth tokens and API keys at rest. **Back it up; losing it means reconnecting every integration** |
| `SESSION_SECRET` | (required) | At least 32 characters |

The binary refuses to start if a required value is missing or malformed, and says which one.

## Upgrades and migrations

Replace the image and start it. Migrations are embedded in the binary and run at startup in order, one transaction each; the schema version is tracked in SQLite's `PRAGMA user_version`. GoCasa refuses to start on a database written by a newer version: run the newer image again, or restore a backup.

## HTTPS

GoCasa speaks plain HTTP and expects your reverse proxy to terminate TLS. It ignores `X-Forwarded-*` headers and takes its own address from `BASE_URL`, so set `BASE_URL` to the `https://` name people actually type.

**Why bother:** over plain HTTP everything works except Screen Wake Lock and camera motion-wake on the Wall display (browsers allow them only in a secure context) and Connect Google (Google requires an `https` redirect on a real domain). Settings tells you HTTPS turns them on.

**Recipe:**

1. Pick a name on a domain you own, e.g. `casa.example.com`.
2. Point it at the GoCasa host's LAN IP in your local DNS (router, Pi-hole, AdGuard). It needn't resolve publicly: the Google redirect happens in the browser.
3. Give a reverse proxy a real certificate for that name, e.g. Let's Encrypt with a DNS-01 challenge (no ports open to the internet).
4. Set `BASE_URL=https://casa.example.com` and restart GoCasa.

### Example: Nginx Proxy Manager

1. **SSL Certificates → Add → Let's Encrypt**: domain `casa.example.com`, turn on **Use a DNS Challenge**, pick your DNS provider and paste its API token.
2. **Hosts → Proxy Hosts → Add**: domain `casa.example.com`, scheme `http`, forward host the GoCasa host's IP, port `8080`. On the **SSL** tab pick the certificate and turn on **Force SSL**.

No custom Nginx config is needed: GoCasa's live-update stream handles proxy buffering and idle timeouts itself.

### Remote access

Reaching GoCasa from outside your home network is out of scope. That is your homelab's job (a tunnel, a VPN, an authenticating proxy); GoCasa documents none of them.

## Development

```sh
go test ./...
go run github.com/a-h/templ/cmd/templ@v0.3.1070 generate   # after editing any .templ file; *_templ.go is committed
go run ./cmd/gocasa      # needs the env vars above, e.g. DATA_DIR=./data
```

Tests build the real app through `internal/apptest`: a temp SQLite file, a fake clock, fake upstream URLs and `RunDue` to run scheduled work on demand.
