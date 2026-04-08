# Base image for integration tests.
# Contains the Go toolchain, postgresql-client, and a plakar binary installed
# at a specific commit.  The image is kept between runs so that the
# go install step (which clones and builds plakar) is only paid once.
#
# Build:
#   docker build --build-arg PLAKAR_SHA=abc1234 -t plakar-test -f tests/plakar.Dockerfile .
ARG PLAKAR_SHA=main

FROM golang:1.25

ARG PLAKAR_SHA

RUN apt-get update && \
    apt-get install -y --no-install-recommends postgresql-client && \
    rm -rf /var/lib/apt/lists/*

RUN go install github.com/PlakarKorp/plakar@${PLAKAR_SHA}
