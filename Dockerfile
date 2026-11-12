# syntax=docker/dockerfile:1.7
FROM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags='-s -w' \
    -o /out/splat \
    ./cmd/splat

# Pre-create the runtime directories owned by the nonroot uid. Distroless has
# no shell to mkdir at runtime, and a freshly created *named* volume inherits
# the ownership of the image directory it is mounted over — so without this the
# /cache volume lands owned by root and the nonroot process gets EACCES when it
# writes thumbnail temp files. Build the skeleton here, then COPY --chown it in.
RUN mkdir -p /skel/cache /skel/data /skel/etc/splat

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/splat /usr/local/bin/splat
COPY --from=build --chown=65532:65532 /skel/cache /cache
COPY --from=build --chown=65532:65532 /skel/data /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/splat"]
CMD ["--config", "/etc/splat/config.yaml"]

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD ["/usr/local/bin/splat", "healthcheck", "--config", "/etc/splat/config.yaml"]
