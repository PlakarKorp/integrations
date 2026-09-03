GO      = go
EXT     =
PLAKAR  ?= plakar
VERSION ?= v1.0.0

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := forgejo_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o forgejoImporter${EXT} ./plugin/importer
	${GO} build -v -o forgejoExporter${EXT} ./plugin/exporter

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

test:
	${GO} test ./...

clean:
	rm -f forgejoImporter forgejoExporter forgejo_*.ptar

.PHONY: all build package test clean
