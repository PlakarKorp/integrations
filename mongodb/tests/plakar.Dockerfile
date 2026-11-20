# Image for integration tests: plakar and the mongodb plugin built from the
# source tree, plus the MongoDB tools the plugin shells out to.
# The image is kept between runs as plakar-mongodb-test:<PLAKAR_SHA>, so
# later runs only rebuild the layers that changed.
ARG PLAKAR_SHA=6574a2c153c0b35db2ed98a2490aa9dda86bddbe
ARG MONGO_IMAGE=mongodb/mongodb-community-server:8.0-ubuntu2204@sha256:fe5f7a181953d5e60fd4f35ff221700a121703df48f01221be1316ebe949b365

# MongoDB's Debian packages are amd64 only, so take the tools from the server
# image instead: same version as the server, and built for both arches.
FROM ${MONGO_IMAGE} AS mongo

FROM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf

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
