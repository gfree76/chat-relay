# chat-relay

A minimal, **zero-knowledge** push relay for the E2E chat app. It maps a `userId`
to an FCM registration token and forwards end-to-end-encrypted ciphertext to that
user via Firebase Cloud Messaging. It never sees plaintext or keys — public keys
are exchanged out-of-band (QR) between clients, so the relay only ever routes.

## Why a relay at all?
FCM lets a device *receive* pushes with no server, but *sending* a push requires
privileged credentials (a service account) that must never ship in the app. So a
trusted sender is unavoidable — this is it. Content-blind, in-memory `userId →
fcmToken` map.

## API
| Method | Path | Body | Purpose |
|--------|------|------|---------|
| `POST` | `/register` | `{"userId":"…","fcmToken":"…"}` | remember where to push for a user |
| `POST` | `/send` | `{"toUserId":"…","ciphertext":"…"}` | forward ciphertext to that user |
| `GET`  | `/healthz` | — | liveness check |

## Layout
```
main.go        wiring + config      store.go     userId → fcmToken map
server.go      handlers + routing   push.go      FCM send (stub for now)
*_test.go      unit tests           integration_test.go  full round-trip (build tag)
```
Crypto is nowhere here — the relay is content-blind by design.

## Local development
```bash
cp .env.example .env      # optional; ADDR etc.
make run                  # go run . --addr :8080
make help                 # list all targets
```
Smoke it:
```bash
curl -X POST localhost:8080/register -d '{"userId":"geoff","fcmToken":"tok"}'
curl -X POST localhost:8080/send     -d '{"toUserId":"geoff","ciphertext":"AAAA"}'
```

## Testing
```bash
make test              # unit tests, race detector
make test-integration  # full HTTP round-trip (tagged 'integration')
make cover             # coverage summary
make lint              # go vet + gofmt check
```
Tests inject a fake pusher, so they never touch real FCM.

## Container
```bash
make docker            # distroless, non-root, static binary
```

## CI/CD
- **CI** (`.github/workflows/ci.yml`, on PRs): fmt, vet, unit + integration tests, `go build`, `docker build`.
- **Deploy** (`.github/workflows/deploy.yml`, on merge to main): build + push image to
  **GHCR**, then SSH to the Hetzner box and `docker compose pull && up`.

### Deploy prerequisites (set as repo secrets)
| Secret | Meaning |
|--------|---------|
| `SSH_HOST` | Hetzner box host/IP |
| `SSH_USER` | deploy user |
| `SSH_KEY` | that user's private key (ed25519) |
| `SSH_KNOWN_HOSTS` | `ssh-keyscan <host>` output (pins the host key) |
| `DEPLOY_DIR` | dir on the box holding this repo's `compose.yaml` |

On the box: `compose.yaml` runs the GHCR image; wire it into the existing reverse
proxy (Traefik/nginx/Caddy/Authentik proxy) so the phones reach it over HTTPS at a
stable hostname (e.g. `relay.itsalwaysreefreemas.org`).

## Status / TODO
- [ ] Implement `fcmPush` — the FCM HTTP v1 call (`golang.org/x/oauth2/google` for the SA token).
- [ ] Persist the store (SQLite/Bolt) so registrations survive restarts.
- [ ] Finalize reverse-proxy + DNS on the box.
