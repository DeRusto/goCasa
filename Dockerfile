# syntax=docker/dockerfile:1
# Build on the runner's arch and cross-compile; QEMU-emulated go builds are very slow.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
RUN apk add --no-cache ca-certificates
ARG GOPROXY=https://proxy.golang.org,direct
COPY . .
# templ output (*_templ.go) is committed, so no generate step here. go build fetches
# only the binary's modules (go mod download would also pull the templ CLI's tools).
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/gocasa ./cmd/gocasa \
 && mkdir -p /out/data

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/gocasa /gocasa
# A named volume inherits this ownership on first mount.
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV ADDR=:8080 DATA_DIR=/data
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/gocasa", "healthcheck"]
ENTRYPOINT ["/gocasa"]
