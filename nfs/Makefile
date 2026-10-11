GO      = go
EXT     =

PLAKAR  ?= plakar
VERSION ?= v1.1.1

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := nfs_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o nfsImporter${EXT} ./plugin/importer
	${GO} build -v -o nfsExporter${EXT} ./plugin/exporter
	${GO} build -v -o nfsStorage${EXT} ./plugin/storage

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm nfs

install: package
	$(PLAKAR) pkg add -allow-unsigned ./$(PTAR)

reinstall: uninstall install

test:
	${GO} test -v ./...

clean:
	rm -f nfsImporter nfsExporter nfsStorage nfs_*.ptar

.PHONY: all build package uninstall install reinstall test clean
