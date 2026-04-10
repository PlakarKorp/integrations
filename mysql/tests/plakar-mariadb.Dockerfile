# Image for MariaDB integration tests.
# Uses golang:bookworm as the base so Go is pre-installed, then adds the
# MariaDB client package (mariadb-client) which provides the mariadb and
# mariadb-dump binaries used by the mysql+mariadb connector.
#
# Build manually:
#   docker build --build-arg PLAKAR_SHA=main -t plakar-mariadb-test -f tests/plakar-mariadb.Dockerfile .
ARG PLAKAR_SHA=main

FROM golang:1.25-bookworm

ARG PLAKAR_SHA

RUN apt-get update && \
    apt-get install -y --no-install-recommends mariadb-client && \
    rm -rf /var/lib/apt/lists/*

RUN go install github.com/PlakarKorp/plakar@${PLAKAR_SHA}

COPY . /src

RUN set -e && \
    mkdir -p /tmp/mysqlpkg && \
    cd /src && \
    go build -o /tmp/mysqlpkg/mysqlImporter ./plugin/mysql-importer && \
    go build -o /tmp/mysqlpkg/mysqlExporter  ./plugin/mysql-exporter && \
    go build -o /tmp/mysqlpkg/mariadbImporter ./plugin/mariadb-importer && \
    go build -o /tmp/mysqlpkg/mariadbExporter  ./plugin/mariadb-exporter && \
    cp /src/manifest.yaml /tmp/mysqlpkg/ && \
    cd /tmp/mysqlpkg && \
    PTAR="mysql_v0.0.1_$(go env GOOS)_$(go env GOARCH).ptar" && \
    plakar pkg create ./manifest.yaml v0.0.1 && \
    plakar pkg add "./${PTAR}" && \
    rm -rf /tmp/mysqlpkg /src
