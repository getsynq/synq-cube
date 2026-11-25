# The image of a release. The release workflow builds the binaries with GoReleaser and copies
# them to dist/synq-cube-<arch>, so the binary in the image is the binary in the archives.
#
# To build an image from a checkout:
#
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/synq-cube-amd64 .
#   docker build --platform linux/amd64 -t synq-cube .

# The certificates are the same files on each architecture. This stage runs on the architecture
# of the builder, so the build of the arm64 image needs no emulation.
FROM --platform=$BUILDPLATFORM ubuntu:24.04 AS certs
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get -qq update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

FROM ubuntu:24.04 AS runtime
LABEL org.opencontainers.image.source="https://github.com/getsynq/synq-cube"
LABEL org.opencontainers.image.licenses="Apache-2.0"

COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

ARG TARGETARCH
COPY --chmod=0755 dist/synq-cube-${TARGETARCH} /bin/synq-cube

# The command needs no root access. /tmp is writable for `--output`.
USER 65534:65534
WORKDIR /tmp

ENTRYPOINT ["/bin/synq-cube"]
CMD ["upload"]
