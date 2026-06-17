# Splat — Design Document

A Dockerized web UI for quick-and-dirty image edits across multiple images in
sequence. Built with Go + htmx + Pico CSS + Cropper.js.

## 1. Scope

### In scope (v1)

- **Sources:** local volume-mounted directory, AWS S3. Exactly one active
  source per deployment.
- **Listing:** recursive walk of the source, alphabetical order, file
  extensions allowlisted to `.jpg`, `.jpeg`, `.png`, `.webp`.
- **Operations:** preview thumbnail, delete, crop (with ratio presets + custom
  + free), rotate 90°/180°, flip horizontal/vertical, save (in-place or copy).
- **Concurrency:** optimistic concurrency via SHA-256 content hash on save.
- **Single active image** with keyboard navigation; modal confirmations for
  destructive operations; toast undo (5s) for delete.
- **Frontend:** Pico CSS, htmx, Cropper.js — delivered via CDN with SRI hashes.
- **Backend:** Go (stdlib `net/http`), single binary, distroless container as
  non-root UID 65532, configurable port (default 8080).
- **Config:** YAML with `${ENV_VAR}` interpolation. Located via `--config`
  flag, falling back to `$SPLAT_CONFIG`, falling back to `./config.yaml`.
  Validated at startup; fail-fast on error.
- **Logging:** JSON via `log/slog` to stdout.
- **Health:** `GET /healthz` returns 200 OK.
- **Tests:** unit (config, suffix, hash, crop math), httptest handler tests,
  local-source integration via `t.TempDir()`. GitHub Actions CI runs
  `go test ./...`, `go vet`, `golangci-lint`. No coverage gate.

### Out of scope (v1)

- Resize/scale operations, filters (brightness/contrast/etc.), annotations,
  text overlay, multi-select / bulk operations, edit history beyond the 5s
  delete toast, EXIF preservation, authentication, rate limiting, metrics,
  audit logs, runtime source switching, multi-tenant deployment.
- Mobile / touch support — desktop-only.
- Recursive S3 pagination optimization — re-listing per batch is accepted cost.

## 2. Architecture

### 2.1 Project layout

```
cmd/splat/main.go            # entry point, flag parsing, lifecycle
internal/config/             # YAML loading, validation, env interpolation
internal/source/             # Source interface, local + S3 implementations
internal/imageops/           # Operation interface + Crop, Rotate, Flip impls
internal/format/             # Format-handler registry (jpeg, png, webp)
internal/server/             # HTTP handlers, routing, templates
internal/server/templates/   # html/template files (embedded)
internal/server/static/      # Pico-extension CSS, app.js — embedded
web/                         # (none — assets live under internal/server/static)
```

### 2.2 Source interface

```go
type Source interface {
    List(ctx context.Context) ([]Entry, error)
    Get(ctx context.Context, key string) (io.ReadCloser, Metadata, error)
    Put(ctx context.Context, key string, r io.Reader, contentType string) error
    Delete(ctx context.Context, key string) error
}

type Entry struct {
    Key     string    // canonical, slash-separated, source-relative
    Size    int64
    ModTime time.Time
    ETag    string    // local: hex of "<size>-<mtime-unix-nano>"; S3: object ETag
}

type Metadata struct {
    Size        int64
    ContentType string
    ETag        string
}
```

Two implementations: `localSource` (rooted at a configured path) and
`s3Source` (bucket + optional prefix). Listings are filtered to the
allowlisted extensions before returning.

**Save semantics** (used by handlers, not part of the interface):
1. Read original via `Get`. Optimistic check: caller-supplied SHA-256 must
   match a hash computed at this point (i.e. just before the rewrite).
2. Apply operation in memory.
3. Encode via the format-handler registry. If input was `.webp`, output is
   `.png` (key extension changes accordingly).
4. **In-place mode:** `Put` to the new key (which may equal the old key for
   non-WebP inputs). If the new key differs from the old (WebP case),
   `Delete` the old key after successful `Put`.
5. **Copy mode:** compute target key with `-edited` (or `-edited-N` on
   collision; counter increments until a free key is found). `Put` to the
   target.

**Atomicity:** the local source implements `Put` as
write-temp-file-and-rename within the same directory (`os.Rename` is atomic
on the same filesystem). S3 `PutObject` is atomic by API contract.

### 2.3 Operation interface

```go
type Operation interface {
    Name() string
    Apply(img image.Image) (image.Image, error)
}
```

Implementations:
- `Crop{X, Y, W, H int}` — crops to absolute pixel rect on the original.
- `RotateCW90`, `RotateCCW90`, `Rotate180` — rotate by fixed amounts.
- `FlipHorizontal`, `FlipVertical`.

Internal primitives: rotate-by-90-cw and flip-horizontal. CCW90 = three
CW90s; 180 = two CW90s; flip-vertical = rotate180 + flip-horizontal. Five
distinct user-facing operations, two primitives.

### 2.4 Format-handler registry

```go
type Format struct {
    Ext         string             // ".jpg", ".png", ...
    ContentType string             // "image/jpeg", ...
    Decode      func(io.Reader) (image.Image, error)
    Encode      func(io.Writer, image.Image) error
    OutputExt   string             // for WebP, "" forces ".png" on save
}

var registry = map[string]Format{...}
```

JPEG quality is read from config (default 90); the JPEG encoder closes over
that value. WebP entry has `Decode` only and an `OutputExt` of `.png` to
trigger the format change on save. Adding GIF / future formats = one
registration.

### 2.5 HTTP routes (htmx fragments unless noted)

| Method | Path                          | Returns        | Purpose                            |
|--------|-------------------------------|----------------|------------------------------------|
| GET    | `/`                           | full HTML page | initial app shell                  |
| GET    | `/strip?offset=&limit=`       | fragment       | one batch of preview thumbnails    |
| GET    | `/thumb/{key}`                | image bytes    | resized cached thumbnail           |
| GET    | `/image/{key}`                | image bytes    | original (for editor)              |
| GET    | `/editor/{key}`               | fragment       | editor pane HTML for `key`         |
| POST   | `/apply/{key}`                | fragment       | apply queued op (crop/rotate/flip) |
| DELETE | `/image/{key}`                | empty / toast  | delete                             |
| GET    | `/healthz`                    | 200 OK         | health                             |

Apply request body (form-encoded or htmx values):
- `op` ∈ {`crop`, `rotate-cw`, `rotate-ccw`, `rotate-180`, `flip-h`, `flip-v`}
- `mode` ∈ {`inplace`, `copy`}
- `hash` (hex SHA-256 of bytes when the editor was loaded)
- `x`, `y`, `w`, `h` for crop — pixel coordinates on the original

A 409 Conflict is returned when `hash` does not match; the response fragment
shows an inline error with a "reload" action.

### 2.6 Thumbnails

On-disk LRU cache rooted at the configured `thumbnail_cache_dir`
(default `/tmp/splat-thumbs`). Cache key = SHA-256 of
`<source-id>|<key>|<etag-or-mtime>`. Cap configurable in YAML
(default 1 GB), evicted oldest-access first. Thumbnail height configurable
(default 200 px), width preserves aspect ratio. Resizing uses
`golang.org/x/image/draw` with `draw.CatmullRom`.

### 2.7 Listing & lazy loading

Each `/strip` request re-lists the source from scratch with the configured
extension filter, sorts alphabetically by key, slices `[offset:offset+limit]`,
and renders a fragment of `<img>` tags pointing at `/thumb/{key}`. htmx
infinite-scroll loads the next batch on horizontal scroll near the right edge.
For S3 with very large prefixes, this re-lists per batch — accepted cost,
documented in README.

### 2.8 Filename suffix logic

On save in **copy mode**, given source key `path/to/name.ext`:
1. Candidate = `path/to/name-edited.ext` (or `.png` if input was `.webp`).
2. If candidate exists in the source, try `name-edited-1.ext`,
   `name-edited-2.ext`, ... until a free key is found.
3. Counter is unbounded but practically capped at, say, 9999 with a clear
   error if exceeded (defense-in-depth — you're not editing 10,000 copies of
   one image).

The `-edited` suffix is configurable in YAML (`copy_suffix`, default
`-edited`). Empty suffix is rejected at config load.

In **in-place mode**, target key = source key (for non-WebP), or
`<stem>.png` for WebP inputs (the original `.webp` is deleted after the new
`.png` is written).

## 3. Frontend

### 3.1 Layout

CSS Grid:

```
┌─────────────────────────────────────────┐
│  Editor pane (Cropper.js or rotate UI)  │  flex: 1 1 auto
│                                         │
├─────────────────────────────────────────┤
│  Toolbar: ratios, ops, mode toggle      │  fixed
├─────────────────────────────────────────┤
│  Strip (horizontal scroll, lazy load)   │  ~240 px (200 thumbs + chrome)
└─────────────────────────────────────────┘
```

### 3.2 JavaScript

Three vendored / CDN scripts:
- htmx 2.x with SRI.
- Cropper.js 1.x with SRI.
- One small `app.js` (≤ 200 LOC): keyboard handler, toast machinery, mode
  toggle persistence (localStorage), Cropper.js wiring.

Keyboard shortcuts:
- ←/→ : prev/next image in the strip.
- Enter : apply current crop selection.
- Esc : clear current crop selection.
- Delete / Backspace : delete current image (toast-undo).
- 1-9 : select ratio preset by index.

Toasts: a single `<aside id="toasts" role="status" aria-live="polite">`
container; htmx response header `HX-Trigger: showToast` with a JSON payload
(`{message, kind, undoUrl?}`) drives append + auto-remove + optional undo
button. The DELETE request is fired by the undo timeout's
`setTimeout`, so closing the tab cancels the delete.

### 3.3 Accessibility

- Semantic HTML throughout (Pico-friendly).
- `<dialog>` with `showModal()` for confirmations; focus trap + restore.
- `alt` on every thumbnail (the basename).
- `title` on every thumbnail (the full key, the desktop hover tooltip).
- `aria-live="polite"` on the toast container.
- Visible focus rings preserved (no `outline: none`).

### 3.4 Ratio presets

```
Free, Original, 1:1, 4:5, 5:4, 4:3, 3:4, 3:2, 2:3, 16:9, 9:16
```

Plus a custom-ratio input box accepting `W:H` (positive integers, max 4
digits each). Invalid input is rejected client-side and ignored on the server.

## 4. Configuration

`config.yaml`:

```yaml
server:
  port: 8080                       # default 8080
  shutdown_timeout: 10s

source:
  type: local                      # "local" | "s3"
  local:
    root: /data
  s3:
    bucket: my-bucket
    prefix: photos/                # optional
    region: eu-central-1
    auth: static                   # only "static" in v1
    access_key: ${AWS_ACCESS_KEY_ID}
    secret_access_key: ${AWS_SECRET_ACCESS_KEY}

editing:
  copy_suffix: -edited
  jpeg_quality: 90                 # 1..100

thumbnails:
  cache_dir: /cache
  height_px: 200
  cache_max_bytes: 1073741824      # 1 GiB
```

`${VAR}` interpolation runs over all string fields after YAML parsing,
before validation. Unknown variables fail validation. Validation enforces:
- Exactly one of `source.local` / `source.s3` populated, matching `type`.
- `copy_suffix` non-empty.
- `jpeg_quality` in `[1, 100]`.
- `port` in `[1, 65535]`.
- Local `root` exists and is a readable directory at startup.
- S3 credentials non-empty when `type: s3` and `auth: static`.

## 5. Concurrency model

- **Optimistic save** via `If-Match`-style hash check (computed and verified
  on the server side).
- **No server-side session state.** The editor pane is loaded with the
  current hash embedded in the form; the apply handler reads the original
  bytes again, recomputes the hash, and 409s on mismatch.
- **Single active image** in the UI. Switching thumbnails silently discards
  any in-progress crop selection.

## 6. Docker & deployment

Multi-stage build:

```dockerfile
FROM golang:1.23 AS build
WORKDIR /src
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/splat ./cmd/splat

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/splat /usr/local/bin/splat
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/splat", "--config", "/etc/splat/config.yaml"]
```

Volumes the user is expected to mount:
- `/data` — source directory (when using local source).
- `/cache` — thumbnail cache (optional but recommended for warm restarts).
- `/etc/splat/config.yaml` — config file.

Healthcheck (Compose example):

```yaml
healthcheck:
  test: ["CMD", "/usr/local/bin/splat", "healthcheck"]
  interval: 30s
  timeout: 3s
  retries: 3
```

(`splat healthcheck` is a tiny CLI subcommand that issues `GET /healthz`
against `localhost:$PORT` from inside the container — distroless has no
`curl`/`wget`, so we provide the probe ourselves.)

Graceful shutdown: SIGTERM → `http.Server.Shutdown(ctx)` with a 10s
deadline (configurable).

## 7. Testing

- **Unit:** `internal/config` (parsing, validation, env interpolation),
  `internal/source/local` (suffix collision counter, key path canonicalization),
  `internal/imageops` (crop math, rotate/flip primitives), `internal/server`
  helpers (hash check, key parsing).
- **Integration:** `internal/source/local` with `t.TempDir()` fixtures for
  list/get/put/delete round-trips. S3 source has no automated tests in v1
  (manual verification only).
- **Handler:** `internal/server` with `httptest.NewServer` and a real local
  source on `t.TempDir()`. Asserts on rendered HTML fragments and HTTP status
  codes including the 409 path.
- **CI:** GitHub Actions, single workflow `.github/workflows/ci.yaml`,
  matrix Go 1.23 on `ubuntu-latest`. Steps: `go vet`, `go test ./...`,
  `golangci-lint run`. No coverage gate.

## 8. Observability

- **Logs:** `log/slog` with JSON handler to stdout. Standard fields:
  `time`, `level`, `msg`, plus `request_id`, `method`, `path`, `status`,
  `duration_ms`, `source_key` where applicable.
- **Metrics:** none.
- **Tracing:** none.
- **Health:** `/healthz` returns 200 unconditionally; the source connection
  is validated at startup, and process liveness implies operability.

## 9. Error UX

- Operation errors (decode failed, save failed, hash mismatch) surface as
  inline `<aside role="alert">` in the editor pane via htmx swap.
- Transient/notification events (delete pending, delete cancelled, save
  succeeded) surface as toasts.
- Startup errors (bad config, source unreachable) cause exit with non-zero
  status and a structured error log line — never reach the UI.

## 10. License

MIT-derivative, SPDX `LicenseRef-MIT-NoAI`. Standard MIT body, plus an
explicit additional restriction prohibiting use as training data for
machine-learning systems. README documents the non-OSI status and that
the additional clause is legally untested.

## 11. Documented surprises (READ THIS section in the README)

- WebP files are saved as PNG (`.webp` → `.png`). In-place save deletes the
  original `.webp`.
- EXIF metadata is stripped on every save.
- Edited files appear in the next listing; collisions get `-edited-N`.
- Empty subdirectories are not pruned after delete (local source).
- Multi-tab edits use optimistic concurrency: a second-tab save loses, with a
  clear "reload required" error.
- The CDN-delivered front-end requires network at runtime to bootstrap.
- The container has no built-in auth; deploy behind a reverse proxy / VPN.
