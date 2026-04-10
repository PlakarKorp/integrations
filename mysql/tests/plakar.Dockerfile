# Image for integration tests.
# Contains the Go toolchain, mysql-client, a plakar binary, and the
# mysql plugin built and installed from the source tree.
# The image is kept between runs (KeepImage: true) so that subsequent test
# runs only rebuild layers that changed.
#
# We use mysql:8 as the base image (rather than a generic Debian/Go image)
# to ensure that the mysql and mysqldump binaries are the official MySQL 8
# client tools. Using the default Debian package (default-mysql-client)
# installs MariaDB's mysqldump instead, which generates dumps incompatible
# with MySQL 8 (e.g. it emits INSERT statements for generated columns such
# as engine_cost.default_value, which MySQL 8 rejects with ERROR 3105).
#
# Build manually:
#   docker build --build-arg PLAKAR_SHA=main -t plakar-mysql-test -f tests/plakar.Dockerfile .
ARG PLAKAR_SHA=main

FROM mysql:8

ARG PLAKAR_SHA

RUN microdnf install -y golang && microdnf clean all

RUN go install github.com/PlakarKorp/plakar@${PLAKAR_SHA}

ENV PATH="/root/go/bin:${PATH}"

COPY . /src

RUN set -e && \
    mkdir -p /tmp/mysqlpkg && \
    cd /src && \
    go build -o /tmp/mysqlpkg/mysqlImporter ./plugin/mysql-importer && \
    go build -o /tmp/mysqlpkg/mysqlExporter  ./plugin/mysql-exporter && \
    cp /src/manifest.yaml /tmp/mysqlpkg/ && \
    cd /tmp/mysqlpkg && \
    PTAR="mysql_v0.0.1_$(go env GOOS)_$(go env GOARCH).ptar" && \
    plakar pkg create ./manifest.yaml v0.0.1 && \
    plakar pkg add "./${PTAR}" && \
    rm -rf /tmp/mysqlpkg /src
