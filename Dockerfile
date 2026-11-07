# static-server: one stdlib-only Go binary on distroless/static (nonroot, no shell). Build images
# FROM this one: COPY the site to /public and, when the defaults don't fit, a config to
# /etc/static-server/config.json (see README.md).
#
# Updates: Dependabot bumps the golang and distroless digests.

FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=compile /out/static-server /usr/bin/static-server
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD ["/usr/bin/static-server", "-healthcheck"]
ENTRYPOINT ["/usr/bin/static-server"]
