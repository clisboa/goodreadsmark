FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.3.1
ENV GOMAXPROCS=2 GOMEMLIMIT=1200MiB
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -p 2 -trimpath -ldflags="-s -w -X github.com/clisboa/goodreadsmark/internal/app.Version=$VERSION" \
    -o /out/goodreadsmark ./cmd/goodreadsmark
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -p 2 -trimpath -ldflags="-s -w -X github.com/clisboa/goodreadsmark/internal/app.Version=$VERSION" \
    -o /out/container-smoke ./cmd/container-smoke \
    && mkdir -p /out/data

FROM build AS verify
RUN CGO_ENABLED=0 go test -tags=integration -shuffle=on -count=1 ./...

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
ENV PATH="/usr/local/bin:/usr/bin:/bin" HOME=/tmp TMPDIR=/tmp
COPY --from=build --chown=10001:10001 /out/data /data
COPY --from=build /out/goodreadsmark /usr/local/bin/goodreadsmark
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/goodreadsmark/
USER 10001:10001
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/goodreadsmark"]
CMD ["run"]

FROM runtime AS smoke
COPY --from=build /out/container-smoke /usr/local/bin/container-smoke
ENTRYPOINT ["/usr/local/bin/container-smoke"]
CMD []

FROM runtime AS final
