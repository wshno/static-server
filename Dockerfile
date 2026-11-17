# static-server: one stdlib-only Go binary on distroless/static (nonroot, no shell). Build images
# FROM this one: COPY the site to /public and, when the defaults don't fit, a config to
# /etc/static-server/config.json (see README.md).
#
# Updates: Dependabot bumps the golang and distroless digests.

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./

# `docker build --target test .` runs the test suite on Linux, where the symlink test isn't skipped.
FROM build AS test
COPY examples/ examples/
RUN go vet ./... && go test ./...

FROM build AS compile
ARG TARGETOS
ARG TARGETARCH
ARG APP_VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${APP_VERSION}" -o /out/static-server .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=compile /out/static-server /usr/bin/static-server
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD ["/usr/bin/static-server", "-healthcheck"]
ENTRYPOINT ["/usr/bin/static-server"]
