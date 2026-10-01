# Compost 🪱

> Your Composer downloads, recycled locally.

Compost makes `composer install` fast and resilient by caching everything
Composer downloads (Packagist metadata and package zips from GitHub, GitLab
and Bitbucket) on a server close to you: your LAN, your CI runners, your
Kubernetes cluster. Things go in once and get reused forever. Hence the name.

It is a Go reimplementation of the idea behind
[Velocita](https://github.com/gmta/composer-velocita), and like Velocita it has
two parts:

| Part | What it is | Where |
|------|------------|-------|
| **Compost proxy** (the heap) | A caching reverse proxy, a single static Go binary | `cmd/`, `internal/` |
| **Compost plugin** | A Composer plugin that sends Composer's downloads to the proxy | `plugin/` (PHP, because Composer plugins must be) |

```
composer ── plugin rewrites URL ──▶ Compost proxy ──▶ repo.packagist.org
                                         │       ──▶ api.github.com/repos
                                         │       ──▶ codeload.github.com
                                         ▼       ──▶ gitlab.com, bitbucket.org, ...
                                     heap (disk)
```

## How it works

1. The proxy publishes its mirrors at `GET /mirrors.json`.
2. The plugin reads that document and hooks Composer's `pre-file-download`
   event. Any URL that starts with a mirror's `upstream` is rewritten to
   `<proxy>/<mirror>/<rest>`, for both metadata and dist downloads.
3. The proxy serves the object from its heap, or fetches it, stores it and
   serves it.

URLs are rewritten **at download time only**, so your `composer.lock` keeps
the real upstream URLs and works for anyone without Compost. When the proxy is
unreachable, the plugin prints a warning and Composer downloads directly.

### Caching rules

* **Dists** (zips pinned to a commit) are immutable: `ttl: 0`, cached forever.
* **Metadata** (Packagist `p2/*.json`) has a short TTL (1 minute by default).
  After it expires, Compost revalidates with `If-None-Match` /
  `If-Modified-Since`, so an unchanged file costs one cheap `304`.
* **Upstream down?** Compost serves the stale copy and marks it
  `X-Compost-Cache: STALE`. A `404` from upstream is passed through and never
  cached.
* **Thundering herd**: 50 CI jobs asking for the same zip trigger one upstream
  fetch.
* Every response carries `X-Compost-Cache: HIT | MISS | REVALIDATED | STALE`.

### Not an open proxy

Each mirror can restrict which paths it serves (`allowed_paths`). The GitHub
API mirror only allows `owner/repo/zipball|tarball/ref`, so your
`GITHUB_TOKEN` can't be used to read arbitrary API endpoints through Compost.

## Quick start

### 1. Run the proxy

```bash
# Docker
GITHUB_TOKEN=ghp_xxx docker compose up -d

# or from source
go install github.com/geekette86/compost/cmd/compost@latest
GITHUB_TOKEN=ghp_xxx COMPOST_CACHE_DIR=/var/cache/compost compost
```

Check it: `curl http://localhost:8080/mirrors.json`

A `GITHUB_TOKEN` is optional but strongly recommended: anonymous GitHub API
calls are limited to 60 per hour. A token without any scopes is enough for
public packages.

### 2. Install the plugin

```bash
composer global config repositories.compost vcs https://github.com/geekette86/compost
composer global config allow-plugins.geekette86/compost true
composer global require geekette86/compost:dev-main

composer compost enable https://compost.example.com
composer compost status
```

That's it: every project on that machine now downloads through Compost.

For CI, skip the command and set an environment variable instead:

```bash
export COMPOST_URL=https://compost.internal:8080
```

`COMPOST_DISABLE=1` switches the plugin off for a single run. Use HTTPS in
front of the proxy. For plain `http://` Composer also needs
`composer config -g secure-http false`.

## Configuration

Compost runs with sensible defaults and no config file. To change them, pass
`-config compost.json` (or set `COMPOST_CONFIG`). See
[`compost.example.json`](compost.example.json) and print the effective
configuration with `compost -print-config`.

| Setting | Env | Default | |
|---------|-----|---------|---|
| `listen` | `COMPOST_LISTEN` | `:8080` | Listen address |
| `cache_dir` | `COMPOST_CACHE_DIR` | `./heap` | Where the heap lives |
| `upstream_timeout` | | `5m` | Total time allowed for one upstream fetch |
| `max_object_size` | | `1073741824` | Largest object cached, in bytes (0 = unlimited) |
| `mirrors` | | Packagist, GitHub API, GitHub codeload, GitLab, Bitbucket | Replaces the whole list when set |

Per mirror:

| Field | Meaning |
|-------|---------|
| `name` | URL segment: served at `/<name>/` |
| `upstream` | Base URL being mirrored, ending in `/` |
| `ttl` | `"1m"`, `"24h"`, or seconds. `0`/omitted means cache forever |
| `allowed_paths` | Regexes for the path below `upstream`; others get `403` |
| `headers` | Sent upstream; `${VAR}` is read from the environment, and a header whose variable is empty is dropped |

Adding a private Satis repository or Drupal's packages is just another mirror
entry, and the plugin picks it up from `/mirrors.json` automatically.

## Endpoints

| Path | |
|------|---|
| `GET /mirrors.json` | Discovery document for the plugin |
| `GET /<mirror>/<path>` | Cached upstream object (supports `HEAD`, `Range`, conditional requests) |
| `GET /stats` | Hit, miss, revalidated, stale and error counters per mirror |
| `GET /healthz` | Liveness probe |

## Development

```bash
make test   # Go tests (with -race) + plugin tests
make lint   # gofmt, go vet, php -l
make build  # bin/compost
```

The Go proxy uses only the standard library. The PHP plugin supports PHP 7.4+
and Composer 2.

## Roadmap

* Heap eviction (size limit / LRU) and a `compost gc` command
* Prometheus `/metrics`
* Streaming a cache miss to the first client while it is being stored
* Optional upstream auth for private repositories (Private Packagist, Satis)

## License

MIT
