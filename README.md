# splat

A Dockerized web UI for quick-and-dirty image edits across many images in a
row. Crop (with ratio presets), rotate, flip, delete — over local volumes or
S3.

> **License:** MIT + a no-AI-training restriction. **Not OSI-approved.**
> See [LICENSE](LICENSE).

## Status

v1 in development. See [DESIGN.md](DESIGN.md) for the full spec.

## Quick start

```bash
docker run --rm -p 8080:8080 \
  -v $(pwd)/photos:/data \
  -v $(pwd)/cache:/cache \
  -v $(pwd)/config.yaml:/etc/splat/config.yaml \
  ghcr.io/jo-hoe/splat:latest
```

Open http://localhost:8080.

## Configuration

See [config.example.yaml](config.example.yaml) for the full schema. Highlights:

- One source per deploy (`local` *or* `s3`).
- Recursive walk, alphabetical, allowlist `.jpg`, `.jpeg`, `.png`, `.webp`.
- `${ENV_VAR}` interpolation in any string field.

### AWS S3 permissions

The IAM principal used by splat needs the following permissions. Replace
`my-bucket` with your bucket name.

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "s3:ListBucket",
                "s3:GetObject",
                "s3:HeadObject",
                "s3:PutObject",
                "s3:DeleteObject"
            ],
            "Resource": [
                "arn:aws:s3:::my-bucket",
                "arn:aws:s3:::my-bucket/*"
            ]
        }
    ]
}
```

| Action | Used for |
|---|---|
| `s3:ListBucket` | Listing images in the preview strip |
| `s3:GetObject` | Loading images for editing and thumbnail generation |
| `s3:HeadObject` | Optimistic concurrency check and existence probe |
| `s3:PutObject` | Saving cropped / rotated images |
| `s3:DeleteObject` | Deleting images |

If you only need **read-only** access (browse + download, no edits or deletes),
you can restrict to `s3:ListBucket`, `s3:GetObject`, and `s3:HeadObject`.

## Documented surprises

These behaviors are intentional but worth knowing about up front:

- **WebP files save as PNG.** WebP encoding is not supported by Go's stdlib;
  WebP files are decoded for editing and re-encoded as PNG on save. In-place
  save deletes the original `.webp` file.
- **EXIF metadata is stripped on every save.** Camera info, GPS coordinates,
  orientation flags — all dropped.
- **Edited files reappear in the listing.** A copy save creates
  `name-edited.ext`; subsequent edits get `name-edited-1.ext`,
  `name-edited-2.ext`, ...
- **Empty subdirectories are not pruned** after deleting the last file in
  them (local source).
- **Multi-tab edits use optimistic concurrency.** A second tab's save wins or
  loses based on a content hash; the loser sees a "reload required" error.
- **The web frontend requires network at runtime** to bootstrap the
  CDN-delivered htmx, Cropper.js, and Pico CSS bundles.
- **The container has no built-in authentication.** Deploy behind a reverse
  proxy or VPN.
- **Desktop-only.** No mobile or touch support in v1.

## Development

```bash
go test ./...
go vet ./...
golangci-lint run
```

CI runs the same on every push.
