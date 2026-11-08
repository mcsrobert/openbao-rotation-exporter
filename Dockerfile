FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/openbao-rotation-exporter ./cmd/openbao-rotation-exporter

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
LABEL org.opencontainers.image.source="https://github.com/mcsrobert/openbao-rotation-exporter"
LABEL org.opencontainers.image.description="Prometheus exporter for OpenBao KV v2 rotation deadlines"
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/openbao-rotation-exporter /usr/local/bin/openbao-rotation-exporter
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/openbao-rotation-exporter"]
