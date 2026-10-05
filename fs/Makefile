GO=go
EXT=

PLAKAR  ?= plakar
VERSION ?= v1.1.0

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := fs_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o fs-importer${EXT} ./plugin/importer
	${GO} build -v -o fs-exporter${EXT} ./plugin/exporter

test:
	${GO} vet ./...
	${GO} test ./...

check: test

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm fs

install: package
	$(PLAKAR) pkg add ./$(PTAR)

reinstall: uninstall install

clean:
	rm -f fs-importer fs-exporter fs-storage fs_*.ptar

.PHONY: all build test check package uninstall install reinstall clean
