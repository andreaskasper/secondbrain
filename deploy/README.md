# Deployment examples

Runnable Docker Compose setups. Each directory is self-contained: copy it,
fill in `.env`, and start. A `config.yaml` is optional and only needed for
more than one user - see [`../config.example.yaml`](../config.example.yaml).

| Directory | Setup | When to use it |
| --- | --- | --- |
| [`traefik/`](traefik/) | Traefik + Let's Encrypt | Your own server, your own certificates. The standard case. |
| [`cloudflared/`](cloudflared/) | Cloudflare Tunnel | No open port, no firewall rule, no certificate on the server. The smallest attack surface. |
| [`prometheus/`](prometheus/) | secondbrain + Prometheus | Scraping the metrics endpoint without exposing it. Combines with either of the above. |

Cloudflare's proxy in front of Traefik uses the `traefik/` files unchanged -
what differs is the Cloudflare configuration. The
[deployment guide](https://andreaskasper.github.io/secondbrain/deploy.html)
covers that combination, along with the details that bite: the client IP
behind a proxy, Cloudflare Access breaking MCP clients, SSE timeouts, and how
to get secrets into the container.

## Four rules

1. **`/data` must be writable by UID 65532.** The container runs as the
   distroless `nonroot` user. A named volume handles this by itself; a bind
   mount does not, and needs `sudo chown -R 65532:65532 ./data` before the
   first start. This is the single most common way a first deployment fails,
   and the symptom - a container that exits immediately with a permission
   error on the vault directory - looks like something else.

2. **`SECONDBRAIN_PUBLIC_URL` is the contract.** Every OAuth redirect is
   derived from it. It must be the URL the client uses, including `https://`
   - not the container name and not the internal address.

3. **Never publish port 2020.** In both examples the secondbrain service has
   no `ports:` section on purpose. Publishing it means a plaintext OAuth
   endpoint on the open internet. The same goes for the metrics port when you
   turn metrics on: it is private precisely because nothing maps it.

4. **No `read_only: true`.** Unlike its sibling aegis, this container writes:
   the notes, the SQLite index in `<vault>/.secondbrain/`, and a git commit
   per change. It still runs with `cap_drop: [ALL]`, `no-new-privileges` and
   a non-root user, which is where the actual containment comes from.

## Prometheus

The metrics endpoint is off by default and stays off until
`SECONDBRAIN_METRICS=true`. When you turn it on, the question worth thinking
about is not whether to put a key on it but where to put the endpoint. There
are two protections and they compose:

- **A second listener nobody publishes.** `SECONDBRAIN_METRICS_LISTEN=:9090`
  moves the endpoint off the main listener entirely — that one then returns a
  genuine `404` for the metrics path — and a port with no `ports:` entry is
  reachable from other containers on the same Docker network and from nowhere
  else. [`prometheus/`](prometheus/) is exactly that, ready to run.
- **A shared key.** `SECONDBRAIN_METRICS_KEY`, presented by the scraper as
  `Authorization: Bearer <key>` or `X-API-Key`, at least 16 characters. Use it
  when the scraper cannot join the network, and use both when it can. With
  neither set, the server logs `metrics_unprotected` once at startup and
  starts anyway.

What not to do is add the metrics path to the Traefik router or the tunnel
ingress. The endpoint would then be one mistyped matcher away from the
internet, and the reason the second listener exists is that a port which is
never mapped cannot be reached at all, whatever the key is.

Where the endpoint is mounted is decided at startup, so switching metrics on
is a restart of the container, not a `SIGHUP`.

## Backups

The notes are the product, so back up `/data` and nothing else matters. It is
a directory of Markdown files with a git repository per vault - `tar`, `rsync`
or `git push` to a private remote all work, and none of them need this
software running. Everything under `<vault>/.secondbrain/` is a cache and can
be left out: the index rebuilds itself on the next start.
