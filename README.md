# chat-relay

A minimal, **zero-knowledge** push relay for the E2E chat app. It maps a `userId`
to a Firebase Installation ID (FID) and forwards end-to-end-encrypted ciphertext to that
user via Firebase Cloud Messaging. It never sees plaintext or keys — public keys
are exchanged out-of-band (QR) between clients, so the relay only ever routes.

## Why a relay at all?
FCM lets a device *receive* pushes with no server, but *sending* a push requires
privileged credentials (a service account) that must never ship in the app. So a
trusted sender is unavoidable — this is it. Content-blind `userId → FID`
registry in SQLite.

## API
| Method | Path | Body | Purpose |
|--------|------|------|---------|
| `POST` | `/register` | `{"userId":"…","fid":"…"}` | claim a userId (`200 {"authToken":"…"}`) or refresh its FID (`204`) |
| `POST` | `/send` | `{"toUserId":"…","ciphertext":"…"}` | forward ciphertext to that user (`202`) |
| `GET`  | `/healthz` | — | liveness check |

`/send` delivers an FCM data message with keys `sender` (the authenticated
sender's userId) and `body` (the ciphertext), at high priority. Errors:

| Status | Meaning |
|--------|---------|
| `401` | missing or unknown auth token |
| `404` | recipient not registered |
| `410` | FCM reported the recipient's FID dead (`UNREGISTERED` / `INVALID_ARGUMENT`); its registration, auth token included, was deleted and it must claim its userId again |
| `413` | `sender` + `body` exceed FCM's 4096-byte data payload |
| `502` | any other FCM failure (FCM's status is logged) |

Every `/register` stamps the registration's `registered_at`.

## Authentication
Claiming a `userId` is first-come. `/register` returns an `authToken` once; only its
SHA-256 hash is stored. That token is required to re-register the same `userId` (which
clients do to report a new FID or refresh `registered_at`) and to `/send` at all:

```
Authorization: Bearer <authToken>
```

Anyone may still claim an *unused* `userId` — see Status / TODO.

## Layout
```
main.go        wiring + config      store.go     SQLite registry
server.go      handlers + routing   push.go      FCM HTTP v1 sender
*_test.go      unit tests           integration_test.go  full round-trip (build tag)
```
Crypto is nowhere here — the relay is content-blind by design.

## Configuration
| Env | Flag | Default | Meaning |
|-----|------|---------|---------|
| `ADDR` | `--addr` | `:8080` | listen address |
| `STORE_PATH` | `--store` | `devices.db` | SQLite database |
| `FCM_PROJECT_ID` | — | *required* | Firebase project to send through |
| `GOOGLE_APPLICATION_CREDENTIALS` | — | *required* | service-account key JSON (scope `firebase.messaging`) |

The relay exits at startup if FCM is not configured or the key cannot mint an
access token. The schema migrates forward on open (`PRAGMA user_version`).

## Local development
Running locally sends real pushes, so it needs a service-account key:
```bash
export FCM_PROJECT_ID=org-gfree76-chat
export GOOGLE_APPLICATION_CREDENTIALS=$PWD/secrets/fcm-sa.json   # gitignored
make run                  # go run . --addr :8080
make help                 # list all targets
```
Smoke it:
```bash
TOKEN=$(curl -sX POST localhost:8080/register \
  -d '{"userId":"geoff","fid":"<a real FID>"}' | jq -r .authToken)
curl -X POST localhost:8080/send -H "Authorization: Bearer $TOKEN" \
  -d '{"toUserId":"geoff","ciphertext":"AAAA"}'
```

## Testing
```bash
make test              # unit tests, race detector
make test-integration  # full HTTP round-trip (tagged 'integration')
make cover             # coverage summary
make lint              # go vet + gofmt check
```
Handler tests inject a fake pusher; the FCM sender is tested against local
`httptest` servers standing in for Google's OAuth and FCM endpoints. Nothing
touches real FCM.

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
| `SSH_HOST` | box host/IP |
| `SSH_USER` | deploy user |
| `SSH_KEY` | that user's private key (ed25519) |
| `SSH_KNOWN_HOSTS` | `ssh-keyscan <host>` output (pins the host key) |
| `DEPLOY_DIR` | dir on the box holding this repo's `compose.yaml` |

On the box: `compose.yaml` runs the GHCR image behind nginx + certbot so the
phones reach it over HTTPS at a stable hostname. Full runbook in
[`deploy/README.md`](deploy/README.md).

## Status / TODO
- [x] FCM HTTP v1 delivery to FIDs; dead FIDs are deleted on send.
- [ ] Gate new registrations, so a stranger cannot claim a userId and send.
- [ ] Cull registrations older than the FCM staleness window.
