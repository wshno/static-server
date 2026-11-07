# static-server

A small static file server for containers: a single-page app, a prebuilt web UI, or a directory
another container writes to. It is one Go binary that uses only the standard library, shipped on
`gcr.io/distroless/static-debian13:nonroot` (uid 65532, no shell). The image therefore holds almost
nothing for a vulnerability scanner to flag.

```
ghcr.io/wshno/static-server/server:<version>   # linux/amd64, linux/arm64; cosign-signed, SBOM + SLSA attested
```

## Use it as a base image

```dockerfile
FROM node:26-alpine AS build
# ... npm ci && npm run build ...

FROM ghcr.io/wshno/static-server/server:1.0.0
COPY --from=build /app/dist /public
COPY static-server.json /etc/static-server/config.json
```

Without a config file the server serves `/public` on `:8080` with `Cache-Control: no-cache`,
`X-Content-Type-Options: nosniff` and `Referrer-Policy: strict-origin-when-cross-origin`.

## Behaviour

- Only `GET` and `HEAD` are allowed; anything else gets a 405.
- `..` can't escape the root, and neither can a symlink. Every directory is opened with `os.Root`.
- Dotfiles and dot-directories (`.git`, `.env`) return 404. `/.well-known/` is the exception.
- There are no directory listings. A directory serves its `index.html` or returns 404. A directory
  requested without the trailing slash gets a relative redirect to it.
- The configured `headers` go on every response, including errors.
- A path that matches no file returns 404. With `spaFallback` set, it gets the root `index.html`
  instead, sent as `no-cache`. Rules don't apply to the fallback, so a missing `/assets/x.js` is
  never cached as immutable.
- Range requests, `Last-Modified` and conditional requests work as in `http.ServeContent`.
- `SIGTERM` drains open requests for up to 10 seconds.

## Configuration

The server reads one JSON file. It looks in three places, in this order:

1. the `-config` flag;
2. the `$STATIC_SERVER_CONFIG` variable;
3. `/etc/static-server/config.json`, if that file exists.

Unknown fields are an error, so a typo fails at startup instead of being ignored. `WEB_ROOT` and
`WEB_ADDR` override `root` and `listen`.

| Field          | Default              | Meaning                                                                                                    |
| -------------- | -------------------- | ---------------------------------------------------------------------------------------------------------- |
| `listen`       | `:8080`              | Listen address.                                                                                            |
| `root`         | `/public`            | Directory served at `/`.                                                                                   |
| `spaFallback`  | `false`              | Serve `root/index.html` for unknown paths outside the mounts.                                              |
| `cacheControl` | `no-cache`           | Cache-Control for responses no rule overrides.                                                             |
| `headers`      | nosniff, referrer    | Set on every response. Giving `headers` replaces the defaults; `{}` means none.                            |
| `mounts`       | `[]`                 | `{"prefix": "/data/", "dir": "/run/readsb"}` serves another directory under a prefix. The longest prefix wins, and mounts never fall back to the SPA. |
| `rules`        | `[]`                 | Per-path settings, see below.                                                                              |

Each rule is a regular expression (`match`, Go RE2 syntax) on the cleaned URL path. A directory
index is matched with its trailing slash, e.g. `/docs/`. Rules apply in order. When several match,
a later rule overrides an earlier one field by field, and their `headers` are merged.

| Rule field        | Meaning                                                                                                        |
| ----------------- | -------------------------------------------------------------------------------------------------------------- |
| `cacheControl`    | Cache-Control for matching paths.                                                                              |
| `headers`         | Extra headers for matching paths.                                                                              |
| `contentEncoding` | `gzip`, `br` or `zstd`: the file is stored already compressed under its plain name. The header is sent and the bytes are served unchanged. |
| `compress`        | `true`: gzip on the fly when the client accepts it. Adds `Vary: Accept-Encoding` and ignores `Range`.          |

`compress` and `contentEncoding` exclude each other within a rule, and a later rule replaces
either one.

### Examples

- [`examples/spa.json`](examples/spa.json) is a single-page app with a strict CSP, HSTS,
  immutable `/assets/` and SPA fallback.
- [`examples/tar1090.json`](examples/tar1090.json) is [tar1090](https://github.com/wiedehopf/tar1090)'s
  web UI plus readsb's JSON directory, with the headers from tar1090's nginx.conf. That includes
  `Content-Encoding: gzip` for files readsb writes compressed, and on-the-fly gzip for
  `aircraft.json`.

## Health check

The image has no shell or curl, so the binary checks itself. `static-server -healthcheck` sends
`HEAD /` to the configured address and exits non-zero on a connection error or a 5xx. The image's
`HEALTHCHECK` uses it. In Kubernetes, use an `httpGet` probe instead.

## Development

```sh
go test ./...
docker build --target test .   # on Linux, where the symlink test isn't skipped
docker build -t static-server .
```

Releases: run **Tag and release**. It tags the commit, then dispatches `container.yml`, which
builds both architectures, gates on Grype, pushes, signs and attests. Dependabot bumps the golang
and distroless digests weekly. Cut a release after merging one, so the images built on this one
pick up the fixes.
