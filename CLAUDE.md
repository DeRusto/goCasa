## Agent skills

### Issue tracker

Issues are tracked in this repo's GitHub Issues via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default canonical labels: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

## Testing

Page-script tests run JavaScript in goja (`github.com/dop251/goja`) inside `go test`; see `internal/web/bridge_test.go` for the fake-DOM pattern. CI has no Node.

Run GoCasa locally (a task worktree has no `.env`): `ADDR=127.0.0.1:PORT DATA_DIR=... BASE_URL=http://127.0.0.1:PORT MDNS=0 APP_ENCRYPTION_KEY=$(openssl rand -base64 32) SESSION_SECRET=$(openssl rand -base64 32) go run ./cmd/gocasa`.

After changing Tailwind classes, run `go generate ./internal/web` to rebuild the committed `static/app.css`; it needs the Tailwind v4 standalone CLI on PATH as `tailwindcss` (see README).

Make dev data quickly by POSTing the app's forms with `curl`, a cookie jar and the header `Sec-Fetch-Site: same-origin` (cross-origin protection rejects requests without it). On a fresh `DATA_DIR`, `/setup` creates and unlocks the first Admin. The household timezone defaults to UTC, so use UTC dates for Rounds and events.
