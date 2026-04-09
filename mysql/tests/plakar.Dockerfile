# Image for integration tests.
# Contains the Go toolchain, mysql-client, a plakar binary, and the
# mysql plugin built and installed from the source tree.
# The image is kept between runs (KeepImage: true) so that subsequent test
# runs only rebuild layers that changed.
#
# Build manually:
#   docker build --build-arg PLAKAR_SHA=main -t plakar-mysql-test -f tests/plakar.Dockerfile .
ARG PLAKAR_SHA=main

FROM golang:1.25

ARG PLAKAR_SHA

RUN apt-get update && \
    apt-get install -y --no-install-recommends default-mysql-client ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN go install github.com/PlakarKorp/plakar@${PLAKAR_SHA}

COPY . /src

RUN set -e && \
    mkdir -p /tmp/mysqlpkg && \
    cd /src && \
    go build -o /tmp/mysqlpkg/mysqlImporter ./plugin/importer && \
    go build -o /tmp/mysqlpkg/mysqlExporter  ./plugin/exporter && \
    cp /src/manifest.yaml /tmp/mysqlpkg/ && \
    cd /tmp/mysqlpkg && \
    PTAR="mysql_v0.0.1_$(go env GOOS)_$(go env GOARCH).ptar" && \
    plakar pkg create ./manifest.yaml v0.0.1 && \
    plakar pkg add "./${PTAR}" && \
    rm -rf /tmp/mysqlpkg /src
