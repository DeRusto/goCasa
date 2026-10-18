# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# templ output (*_templ.go) is committed, so no generate step here.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gocasa ./cmd/gocasa \
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
