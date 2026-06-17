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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/splat /usr/local/bin/splat
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/splat"]
CMD ["--config", "/etc/splat/config.yaml"]

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD ["/usr/local/bin/splat", "healthcheck"]
