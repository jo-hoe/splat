# CLAUDE.md — working guidance for the splat repo

## Always ship regression tests with bug fixes

**Every bug fix must come with a regression test that fails before the fix and
passes after.** This is a hard rule for this repo, not a nice-to-have. The
frontend especially has had several bugs that only reproduce through real
browser layout/timing (focus desync, stale thumbnails, Cropper crop-box
misalignment), so a unit test alone is often not enough:

- **Server/Go logic** → add a Go test under the relevant `internal/...` package
  (e.g. `internal/server/handlers_test.go`).
- **Frontend behaviour** (focus, thumbnail refresh, cropper geometry, htmx swap
  interactions) → add a Playwright test under `test/e2e/tests/`. These run via
  the Dockerized harness (`make test-e2e`), never against a local `npm`/exe.

If a bug cannot be reproduced in the e2e harness yet, say so explicitly and add
the closest possible assertion rather than skipping coverage.

## How to run things (environment constraints)

- **Do not run the splat binary locally** and **do not run `npm` on the host.**
  If Node/Playwright is needed, it runs **inside a container only**.
- App for manual testing: `docker compose up -d --build`, then open
  <http://localhost:8080/index.html>. Default compose uses a **named volume**
  for `/cache` (a host bind mount breaks writes for the nonroot uid 65532) and
  mounts `./photos/data:/data` (config `source.local.root` is `/data`).
- e2e suite: `make test-e2e` (compose: real app + Playwright runner, waits on
  the app's healthcheck, `--exit-code-from e2e`).
- Go tests: `make test` (race) / `make test-short`.
- On Windows, Docker host-path mounts need `MSYS_NO_PATHCONV=1` and
  Windows-style paths (e.g. `D:/git/splat/...`).

## Non-obvious gotchas learned the hard way

- **Distroless runtime is nonroot (uid 65532)** and has no shell. `/cache` and
  `/data` must exist in the image owned by 65532 (see `Dockerfile`), otherwise a
  freshly created named volume is root-owned and every `/thumb/*` request 500s
  with `create temp: ... permission denied` — which looks like a frontend bug
  (thumbnails never load, selection state goes haywire) but is really a mount
  permission problem.
- **Static assets are `go:embed`-ed.** Editing `internal/server/static/*.js|css`
  requires rebuilding the image (`docker compose up -d --build`) to take effect —
  there is no live reload.
- The healthcheck must pass `--config /etc/splat/config.yaml`; without it the
  binary falls back to a nonexistent `./config.yaml` and the container never
  reports healthy.

## Cropper.js init is timing-sensitive

The editor builds Cropper.js on `htmx:afterSwap`. Because `#editor` is a grid
row (`1fr`) containing a flex column, the `.cropper-wrap` box is not always laid
out when Cropper measures it, so the absolutely-positioned `.cropper-container`
can land offset (commonly ~one editor-row-height too low) — the crop box then
floats off the image. Any change to `initCropper` in `app.js` must be verified
by clicking several thumbnails **in sequence** (not just one) and asserting the
`.cropper-container` top matches the `.cropper-wrap` top. There is an e2e
regression test for exactly this in `test/e2e/tests/`.
