# Image for integration tests: plakar and the mongodb plugin built from the
# source tree, plus the MongoDB tools the plugin shells out to.
# The image is kept between runs as plakar-mongodb-test:<PLAKAR_SHA>, so
# later runs only rebuild the layers that changed.
ARG PLAKAR_SHA=6574a2c153c0b35db2ed98a2490aa9dda86bddbe
ARG MONGO_IMAGE=mongodb/mongodb-community-server:8.0-ubuntu2204

# MongoDB's Debian packages are amd64 only, so take the tools from the server
# image instead: same version as the server, and built for both arches.
FROM ${MONGO_IMAGE} AS mongo

FROM golang:1.27

ARG PLAKAR_SHA

COPY --from=mongo /usr/bin/mongosh /usr/bin/mongodump /usr/bin/mongorestore /usr/bin/

RUN go install github.com/PlakarKorp/plakar@${PLAKAR_SHA}

COPY . /src

RUN set -e && \
    cd /src && \
    go build -o mongodb-importer ./importer && \
    go build -o mongodb-exporter ./exporter && \
    plakar pkg create ./manifest.yaml v0.0.1 && \
    plakar pkg add -allow-unsigned ./mongodb_v0.0.1_*.ptar && \
    rm -rf /src
