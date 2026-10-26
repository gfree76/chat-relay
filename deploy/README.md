# Deploying the relay

One-time setup. After this, every merge to `main` deploys automatically:
`deploy.yml` builds the image, pushes it to GHCR, then SSHes in and runs
`docker compose pull && up -d`.

## Placeholders

This repo is public, so the box's real address, hostname, and deploy account are
kept out of it. Substitute your own — keep the real values in a password manager
or a gitignored local note, not here:

- `DEPLOY_HOST` — the box's public IP
- `RELAY_HOST`  — the relay's fully-qualified hostname (e.g. `relay.example.com`)
- `DEPLOY_USER` — the unprivileged CI deploy account (this guide assumes `deploy`)

## Why this shape

The box sits on a public IP with nginx terminating TLS via certbot — the same
pattern the other services on it use. If a CDN/tunnel fronts other hosts on the
domain, it need not front this one: each hostname routes independently by its own
DNS record, so this is a plain nginx origin.

The relay therefore binds loopback only and nginx is its sole public entrance.

## 1. DNS

Add an A record for `RELAY_HOST` pointing at `DEPLOY_HOST`. If your DNS provider
proxies records (e.g. Cloudflare's orange cloud), turn the proxy OFF for this
one — otherwise the proxy terminates TLS and clients see its cert instead of the
one certbot installs on the box, and with no origin cert yet you get 521/526.

Verify it resolves to the box before continuing — certbot's HTTP-01 challenge
needs it:

    dig +short RELAY_HOST      # must return DEPLOY_HOST, not a proxy IP

## 2. Deploy user

Runs on the box as an admin. Note what this does and doesn't buy: a revocable,
single-purpose credential and a clean audit trail — **not** privilege isolation.
The `docker` group is root-equivalent (it can bind-mount the host filesystem
into a container). Treat the deploy key as a root key.

    sudo adduser --disabled-password --gecos "" DEPLOY_USER
    sudo usermod -aG docker DEPLOY_USER

## 3. CI SSH key

Run this on your **workstation** (the machine you administer the box from), not
the box. No passphrase — the Actions runner can't type one. Dedicated to this
job so it can be revoked without touching your own access:

    ssh-keygen -t ed25519 -f ~/keystores/chat-relay-deploy -C "chat-relay CI deploy" -N ""

Because the deploy user is `--disabled-password`, `ssh-copy-id` can't bootstrap
it (there's no password to log in with). Install the public half through your own
sudo-capable account instead: copy it to the box, then place it as the deploy
user with correct ownership.

    scp ~/keystores/chat-relay-deploy.pub YOU@DEPLOY_HOST:/tmp/deploy-key.pub
    # then on the box:
    sudo install -d -m 700 -o DEPLOY_USER -g DEPLOY_USER /home/DEPLOY_USER/.ssh
    sudo install -m 600 -o DEPLOY_USER -g DEPLOY_USER \
        /tmp/deploy-key.pub /home/DEPLOY_USER/.ssh/authorized_keys
    rm /tmp/deploy-key.pub

Verify the CI key logs in — no sudo, this is exactly what CI does:

    ssh -i ~/keystores/chat-relay-deploy -o IdentitiesOnly=yes DEPLOY_USER@DEPLOY_HOST 'id; docker ps'

## 4. GHCR pull

If this repo (and thus its GHCR package) is **public**, skip this — public images
pull without auth.

If private, the box must authenticate. Use a GitHub PAT with **`read:packages`
only** — not the token CI uses, nothing with write scope:

    ssh DEPLOY_USER@DEPLOY_HOST
    echo "<PAT>" | docker login ghcr.io -u gfree76 --password-stdin
    docker pull ghcr.io/gfree76/chat-relay:latest

## 5. Deploy directory

`deploy.yml` cds here and runs compose. Create it owned by the deploy user and
leave it empty — the workflow ships `compose.yaml` in:

    sudo -u DEPLOY_USER mkdir -p /home/DEPLOY_USER/chat-relay

The SQLite database lives in `data/`, bind-mounted into the container. The image
runs as uid 65532, so that uid must own the directory:

    sudo mkdir -p /home/DEPLOY_USER/chat-relay/data
    sudo chown 65532:65532 /home/DEPLOY_USER/chat-relay/data

## 6. FCM service-account key

The relay sends pushes as a Firebase service account and refuses to start
without its key. `compose.yaml` sets `FCM_PROJECT_ID=org-gfree76-chat` and
bind-mounts `secrets/fcm-sa.json` read-only at
`/run/secrets/fcm-sa.json` (`GOOGLE_APPLICATION_CREDENTIALS`). If the file is
absent, `docker compose up` fails and so does the deploy.

Copy the key from your workstation, then install it readable only by uid 65532:

    scp fcm-sa.json YOU@DEPLOY_HOST:/tmp/fcm-sa.json
    # then on the box:
    sudo install -d -m 700 /home/DEPLOY_USER/chat-relay/secrets
    sudo install -m 400 -o 65532 -g 65532 /tmp/fcm-sa.json \
        /home/DEPLOY_USER/chat-relay/secrets/fcm-sa.json
    shred -u /tmp/fcm-sa.json

To rotate, install the new key over the old one and
`docker compose restart chat-relay`.

## 7. nginx + TLS

Edit `deploy/nginx/chat-relay.conf` to set `server_name RELAY_HOST;`, then:

    sudo cp deploy/nginx/chat-relay.conf /etc/nginx/sites-available/chat-relay
    sudo ln -s /etc/nginx/sites-available/chat-relay /etc/nginx/sites-enabled/
    sudo nginx -t && sudo systemctl reload nginx
    sudo certbot --nginx -d RELAY_HOST

certbot rewrites the vhost in place with the 443 block and an 80→443 redirect,
and installs its own renewal timer. Nothing to schedule.

## 8. Repo secrets

    gh secret set SSH_HOST   --repo gfree76/chat-relay --body "DEPLOY_HOST"
    gh secret set SSH_USER   --repo gfree76/chat-relay --body "DEPLOY_USER"
    gh secret set DEPLOY_DIR --repo gfree76/chat-relay --body "/home/DEPLOY_USER/chat-relay"
    gh secret set SSH_KEY    --repo gfree76/chat-relay < ~/keystores/chat-relay-deploy
    ssh-keyscan DEPLOY_HOST | gh secret set SSH_KNOWN_HOSTS --repo gfree76/chat-relay

`SSH_KNOWN_HOSTS` pins the host key so the deploy fails closed rather than
trusting whatever answers on that IP. Setting `SSH_HOST` is also what flips the
deploy job from skipped to live — until then it no-ops and the run stays green.

## 9. Verify

    curl -si https://RELAY_HOST/healthz

Watch it serve:

    ssh DEPLOY_USER@DEPLOY_HOST 'cd /home/DEPLOY_USER/chat-relay && docker compose logs -f chat-relay'

## Known gaps

- `compose.yaml` tracks `:latest`. The workflow also tags each image with the
  commit SHA, so pinning is available if a rollback is ever needed.
- Registrations live in `data/devices.db` on the box. Deleting it forces every
  client to re-register and issues them new auth tokens.
