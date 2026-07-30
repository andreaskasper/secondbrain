# Cloudflare Tunnel

No open port, no firewall rule, no certificate on the server. cloudflared
dials out to Cloudflare and requests come back down that connection, so the
machine holding your notes is not addressable from the internet at all.

## Setup

1. In the Zero Trust dashboard: Networks -> Tunnels -> Create a tunnel, then
   Install connector, and copy the token.
2. Add a public hostname to the tunnel pointing at
   `Service: HTTP`, `URL: secondbrain:2020`. HTTP is right - that hop happens
   inside the Docker network and Cloudflare terminates TLS at the edge.
3. `cp .env.example .env`, paste the token, set the hostname and a password.
4. `docker compose up -d`.
5. Add `https://<your host>/mcp` as an MCP server in the client.

`SECONDBRAIN_HOST` must be the hostname on the tunnel and nothing else. The
OAuth redirects are built from it, and a mismatch produces a login that
appears to work and then fails at the token exchange.

## What this buys you and what it costs

There is nothing to scan and nothing to reach. That is a real reduction in
exposure compared with an open 443, and it is also the only setup here that
works behind CGNAT or a router you do not control.

The cost is a dependency: Cloudflare sees every request, and a tunnel outage
is an outage for you. The tunnel token is a credential for your account -
anyone holding it can route your hostname wherever they like - so keep `.env`
out of version control and rotate it if it leaks.

## Do not put Cloudflare Access in front of it

Access intercepts requests with its own login page. An MCP client cannot
answer that page: it expects the OAuth flow this server implements, gets
Cloudflare's HTML instead, and fails with a message that explains nothing.
Authentication is already handled - every route except `/healthz` and the
OAuth metadata requires a bearer token.

## Storage

The compose file uses the named volume `secondbrain-data`, which needs no
ownership fixing. If you switch to the commented bind mount, chown it to
65532 first; the container runs as the distroless nonroot user and will not
start on a directory it cannot write.

Back up `/data`. Markdown plus a git repository per vault, readable without
this software. With `SECONDBRAIN_GIT_REMOTE` set, the backup is a push after
every change and there is nothing else to schedule.
