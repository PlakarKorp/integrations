GO=go
EXT=

PLAKAR  = plakar
VERSION = v1.1.1

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := s3_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o s3Importer${EXT} ./plugin/importer
	${GO} build -v -o s3Exporter${EXT} ./plugin/exporter
	${GO} build -v -o s3Storage${EXT} ./plugin/storage

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm s3

install: package
	$(PLAKAR) pkg add ./$(PTAR)

reinstall: uninstall install

clean:
	rm -f s3Importer s3Exporter s3Storage s3_*.ptar
