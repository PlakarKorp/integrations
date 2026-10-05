# Image for MariaDB integration tests.
# Uses golang:bookworm as the base so Go is pre-installed, then adds the
# MariaDB client package (mariadb-client) which provides the mariadb and
# mariadb-dump binaries used by the mysql+mariadb connector.
#
# Build manually:
#   docker build --build-arg PLAKAR_SHA=main -t plakar-mariadb-test -f tests/plakar-mariadb.Dockerfile .
ARG PLAKAR_SHA=main

FROM golang:1.26-bookworm

ARG PLAKAR_SHA

RUN apt-get update && \
    apt-get install -y --no-install-recommends mariadb-client && \
    rm -rf /var/lib/apt/lists/*

RUN go install github.com/PlakarKorp/plakar@${PLAKAR_SHA}

COPY . /go/src

RUN set -e && \
    cd /go/src && \
    go build -o mysqlImporter ./plugin/mysql-importer && \
    go build -o mysqlExporter  ./plugin/mysql-exporter && \
    go build -o mysqlProxyImporter ./plugin/mysql-proxy-importer && \
    go build -o mysqlProxyExporter  ./plugin/mysql-proxy-exporter && \
    go build -o mariadbImporter ./plugin/mariadb-importer && \
    go build -o mariadbExporter  ./plugin/mariadb-exporter && \
    PTAR="mysql_v0.0.1_$(go env GOOS)_$(go env GOARCH).ptar" && \
    plakar pkg create ./manifest.yaml v0.0.1 && \
    plakar pkg add "./${PTAR}" && \
    rm -rf /go/src