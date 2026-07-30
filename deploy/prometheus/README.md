# Prometheus

secondbrain plus a Prometheus that scrapes it, on a shared internal network.
The metrics are served on a second listener whose port is never published, so
the endpoint exists for the scraper and for nothing else.

## Setup

1. `cp .env.example .env` and fill in a password.
2. Generate a scrape key and write it to both places. It must be at least 16
   characters, and the file must not end in a newline:

   ```bash
   KEY=$(openssl rand -hex 32)
   printf '%s' "$KEY" > secondbrain.key
   echo "SECONDBRAIN_METRICS_KEY=$KEY" >> .env
   chmod 644 secondbrain.key   # Prometheus runs as UID 65534 and has to read it
   ```

   Keep `secondbrain.key` out of version control; the repository's
   `.gitignore` already covers it.
3. `docker compose up -d`.
4. Check the target: an SSH tunnel to `127.0.0.1:9091` and then
   `/targets` in the Prometheus UI. The `secondbrain` job should be `UP`
   within a scrape interval.

Metrics need a restart, not a reload: where the endpoint is mounted is decided
when the process starts, so `docker compose up -d` after a change to
`SECONDBRAIN_METRICS_LISTEN` or `SECONDBRAIN_METRICS_PATH` is required, and
`SIGHUP` will not do it.

## Checking it by hand

From inside the network, with the key:

```bash
docker compose run --rm --entrypoint sh prometheus -c \
  'wget -qO- --header="Authorization: Bearer $(cat /etc/prometheus/secondbrain.key)" \
   http://secondbrain:9090/metrics | head -20'
```

From the host, without it:

```bash
curl http://localhost:9090/metrics     # connection refused - the port is not published
curl http://localhost:2020/metrics     # 404 - the route is not on the main listener
```

Those last two lines are the whole design in two commands.

## Why a second listener rather than the public reverse proxy

The obvious alternative is to leave the metrics on the main listener, let
Traefik or the tunnel route `/metrics` like everything else, and rely on the
key. It works, and it is worse for three reasons.

A published route is reachable by everyone; its safety then rests entirely on
one shared secret staying secret and on the router matching exactly the paths
you meant. A path matcher is a piece of configuration that can be got wrong
silently, and the failure mode is an endpoint that reports how many notes you
have, how many words, how many tasks are open and when your activity stops on
weekends. A port that is never mapped has no such failure mode: there is no
rule to misconfigure, because there is no route.

Second, a public route has to answer. With `SECONDBRAIN_METRICS_LISTEN` set,
the metrics route is not registered on the main listener at all, so a request
for `/metrics` there gets a genuine `404` rather than a `401`. A `401`
announces that something is behind it.

Third, the scraper does not need the public path. Prometheus is already
running next to the container in almost every setup where these metrics are
wanted, and reaching a neighbour by container name is both simpler and
faster than going out to the edge and back in.

The key is still set in this example. It costs nothing, and it means that
another container joining this network later - a log shipper, a backup job,
something with a shell in it - does not get the metrics for free.

## Combining this with Traefik or Cloudflare

Take the `secondbrain` service from [`../traefik/`](../traefik/) or
[`../cloudflared/`](../cloudflared/) as it is, add the four metrics variables
from this compose file, and put the service on the same network as
Prometheus. The public side does not change: TLS is still terminated
upstream, 2020 is still unpublished there, and 9090 is published nowhere.

## What to look at first

The vault gauges, over a long window. `secondbrain_vault_broken_links` and
`secondbrain_vault_orphans` climbing over weeks is a knowledge base decaying,
which is not something anybody usually measures. `secondbrain_vault_notes`
flat while `secondbrain_writes_total` climbs means an agent is editing but
never creating. `secondbrain_truncated_results_total` rising means
`max_response_bytes` is too small for the way the vault is being queried.

Retention in this file is a year, because a month of data does not show any of
that. The series count is tiny - a handful of gauges per vault and a few dozen
counters - so a year costs nothing.

The full list of metrics with types and labels:
<https://andreaskasper.github.io/secondbrain/docs.html#metrics>
