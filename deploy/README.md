# Deploying the relay to the Hetzner box

One-time setup. After this, every merge to `main` deploys automatically:
`deploy.yml` builds the image, pushes it to GHCR, then SSHes in and runs
`docker compose pull && up -d`.

## Why this shape

The box sits on a public IP (`91.99.109.112`) with nginx terminating TLS via
certbot — the same pattern Authentik already uses there. Cloudflare fronts the
*desktop*, not this box, so there's no tunnel involved and nothing to configure
on the Cloudflare side beyond a DNS record.

The relay therefore binds loopback only and nginx is its sole public entrance.
Each hostname on the domain routes independently by its own DNS record; adding
a service here means adding an A record, not touching the tunnel.

## 1. DNS

A record, **DNS-only (grey cloud)** to match `auth`:

    chat-relay.itsalwaysreefreemas.org  A  91.99.109.112

Confirm before continuing — certbot's HTTP-01 challenge needs it resolving:

    dig +short chat-relay.itsalwaysreefreemas.org

## 2. Deploy user

Runs on the box as an admin. Note what this does and doesn't buy: a revocable,
single-purpose credential and a clean audit trail — **not** privilege isolation.
The `docker` group is root-equivalent (it can bind-mount the host filesystem
into a container). Treat the deploy key as a root key.

    sudo adduser --disabled-password --gecos "" deploy
    sudo usermod -aG docker deploy
    sudo mkdir -p /home/deploy/.ssh && sudo chmod 700 /home/deploy/.ssh

## 3. CI SSH key

Generate on the workstation, no passphrase (CI can't type one). Dedicated to
this job so it can be revoked without touching your own access:

    ssh-keygen -t ed25519 -f ~/keystores/chat-relay-deploy -C "chat-relay CI deploy" -N ""

Install the public half:

    ssh-copy-id -i ~/keystores/chat-relay-deploy.pub deploy@91.99.109.112

## 4. GHCR pull credential

`chat-relay` is a private repo, so the box needs to authenticate to pull. Use a
GitHub PAT with **`read:packages` only** — not the token CI uses, and nothing
with write scope:

    ssh deploy@91.99.109.112
    echo "<PAT>" | docker login ghcr.io -u gfree76 --password-stdin

Writes `/home/deploy/.docker/config.json`. Verify:

    docker pull ghcr.io/gfree76/chat-relay:latest

## 5. Deploy directory

`deploy.yml` cds here and runs compose, so this must hold `compose.yaml` and be
owned by `deploy`:

    sudo -u deploy mkdir -p /home/deploy/chat-relay
    # copy compose.yaml from the repo root into /home/deploy/chat-relay/

## 6. nginx + TLS

    sudo cp deploy/nginx/chat-relay.conf /etc/nginx/sites-available/chat-relay
    sudo ln -s /etc/nginx/sites-available/chat-relay /etc/nginx/sites-enabled/
    sudo nginx -t && sudo systemctl reload nginx
    sudo certbot --nginx -d chat-relay.itsalwaysreefreemas.org

certbot rewrites the vhost in place with the 443 block and an 80→443 redirect,
and installs its own renewal timer. Nothing to schedule.

## 7. Repo secrets

    gh secret set SSH_HOST  --repo gfree76/chat-relay --body "91.99.109.112"
    gh secret set SSH_USER  --repo gfree76/chat-relay --body "deploy"
    gh secret set DEPLOY_DIR --repo gfree76/chat-relay --body "/home/deploy/chat-relay"
    gh secret set SSH_KEY   --repo gfree76/chat-relay < ~/keystores/chat-relay-deploy
    ssh-keyscan 91.99.109.112 | gh secret set SSH_KNOWN_HOSTS --repo gfree76/chat-relay

`SSH_KNOWN_HOSTS` pins the host key so the deploy fails closed rather than
trusting whatever answers on that IP. Setting `SSH_HOST` is also what flips the
deploy job from skipped to live — until then it no-ops and the run stays green.

## 8. Verify

    curl -si https://chat-relay.itsalwaysreefreemas.org/healthz

Watch it serve:

    ssh deploy@91.99.109.112 'cd /home/deploy/chat-relay && docker compose logs -f chat-relay'

## Known gaps

- `fcmPush` is still a stub that logs instead of sending. Deliberate: prove the
  path first, wire FCM second. The relay will accept `/register` and `/send` and
  push nothing.
- `compose.yaml` tracks `:latest`. The workflow also tags each image with the
  commit SHA, so pinning is available if a rollback is ever needed.
