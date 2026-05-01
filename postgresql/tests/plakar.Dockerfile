# Image for integration tests.
# Contains the Go toolchain, postgresql-client, a plakar binary, and the
# postgresql plugin built and installed from the source tree.
# The image is kept between runs (KeepImage: true) so that subsequent test
# runs only rebuild layers that changed.
#
# Build manually:
#   docker build --build-arg PLAKAR_SHA=abc1234 -t plakar-test -f tests/plakar.Dockerfile .
ARG PLAKAR_SHA=main

FROM golang:1.25

ARG PLAKAR_SHA

RUN apt-get update && \
    apt-get install -y --no-install-recommends postgresql-client && \
    rm -rf /var/lib/apt/lists/*

RUN go install github.com/PlakarKorp/plakar@${PLAKAR_SHA}

COPY . /src

RUN set -e && \
    mkdir -p /tmp/pgpkg && \
    cd /src && \
    go build -o /tmp/pgpkg/postgresqlImporter    ./plugin/importer && \
    go build -o /tmp/pgpkg/postgresqlExporter    ./plugin/exporter && \
    go build -o /tmp/pgpkg/postgresqlBinImporter ./plugin/binimporter && \
    go build -o /tmp/pgpkg/postgresqlAWSImporter ./plugin/awsimporter && \
    cp /src/manifest.yaml /tmp/pgpkg/ && \
    cd /tmp/pgpkg && \
    PTAR="postgresql_v0.0.1_$(go env GOOS)_$(go env GOARCH).ptar" && \
    plakar pkg create ./manifest.yaml v0.0.1 && \
    plakar pkg add "./${PTAR}" && \
    rm -rf /tmp/pgpkg /src
