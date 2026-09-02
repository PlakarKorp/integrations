GO=go
PLAKAR=plakar
VERSION?=$(shell git describe --tags --always)
GOLANGCI_LINT_VERSION=v2.12.2

all: build

build:
	${GO} build -v -o incusImporter ./plugin/importer
	${GO} build -v -o incusExporter ./plugin/exporter

pkg: build
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

install: pkg
	-${PLAKAR} pkg rm incus
	${PLAKAR} pkg add ./incus_${VERSION}_*.ptar

test:
	${GO} test ./...

# Needs a reachable local Incus daemon (skips otherwise) and rights on
# its socket (incus-admin group).
test-integration:
	${GO} test -tags integration -count=1 -timeout 20m ./...

lint:
	${GO} run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION} run --build-tags integration ./...

clean:
	rm -f incusImporter incusExporter *.ptar
