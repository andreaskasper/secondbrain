# Traefik with Let's Encrypt

The standard case: your own server, your own domain, a certificate that
renews itself.

## Setup

1. Point an A or AAAA record at the server. Ports 80 and 443 have to be
   reachable from the internet - the TLS challenge needs 443, and 80 exists
   only to redirect.
2. `cp .env.example .env` and fill in the hostname, the ACME contact address
   and a password.
3. `docker compose up -d`.
4. Add `https://<your host>/mcp` as an MCP server in the client. The OAuth
   flow does the rest; there is no token to copy anywhere.

The first start creates the default vault with the `wiki-raw` layout and
builds an empty index. `docker compose logs secondbrain` shows one startup
line naming the vaults it found.

## What this buys you and what it costs

Traefik terminates TLS and secondbrain never sees a certificate, which is the
right split: a process holding your notes should not also be holding a
private key. Certificate renewal is automatic and silent.

The cost is that the server needs two open ports and a public DNS record. It
is now a thing on the internet that people will scan. That is survivable -
every route except `/healthz` and the OAuth metadata requires a bearer token,
and login attempts are rate limited per source IP - but if you would rather
not be reachable at all, use [`../cloudflared/`](../cloudflared/) instead.

## Storage

The compose file uses a named volume, `secondbrain-data`. It needs no
ownership fixing and survives `docker compose down`. Switch to the commented
bind mount if you want the Markdown directly in the host filesystem, and
remember the chown: the container runs as UID 65532 and will not start on a
directory it cannot write.

Back up `/data`. It is Markdown plus a git repository per vault, readable
without this software; the `.secondbrain/` directories are caches and can be
skipped.

## Behind Cloudflare's proxy as well

These files work unchanged with the orange cloud switched on. Two things
change in Cloudflare, not here: set SSL mode to Full (strict), and do not put
Cloudflare Access in front of the hostname - it intercepts the OAuth
redirects and MCP clients cannot answer its login page.
