# chat-relay

A minimal, **zero-knowledge** push relay for the E2E chat app. It maps a `userId`
to an FCM registration token and forwards end-to-end-encrypted ciphertext to that
user via Firebase Cloud Messaging. It never sees plaintext or keys — public keys
are exchanged out-of-band (QR) between clients, so the relay only ever routes.

## Why a relay at all?
FCM lets a device *receive* pushes with no server, but *sending* a push requires
privileged credentials (a service account) that must never ship in the app. So a
trusted sender is unavoidable — this is it. It's stateless-ish (an in-memory
`userId → fcmToken` map) and content-blind.

## API
| Method | Path | Body | Purpose |
|--------|------|------|---------|
| `POST` | `/register` | `{"userId":"…","fcmToken":"…"}` | remember where to push for a user |
| `POST` | `/send` | `{"toUserId":"…","ciphertext":"…"}` | forward ciphertext to that user |
| `GET`  | `/healthz` | — | liveness check |

## Run
```bash
go run . --addr :8080
```

## Status / TODO
- [ ] Implement `sendPush` — the FCM HTTP v1 call (needs the Firebase project ID +
      a service-account OAuth token via `golang.org/x/oauth2/google`).
- [ ] Persist the store (SQLite/Bolt) so registrations survive restarts.
- [ ] Deploy to the Hetzner box.

The `sendPush` function is currently a stub that logs instead of pushing, so the
skeleton builds and runs on the standard library alone.
