GO=go
EXT=

PLAKAR  = plakar
VERSION = v1.1.0-beta.2

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := azblob_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o azblob-importer${EXT} ./importer
	${GO} build -v -o azblob-exporter${EXT} ./exporter
	${GO} build -v -o azblob-storage${EXT}  ./storage

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm azblob

install: package
	$(PLAKAR) pkg add ./$(PTAR)

reinstall: uninstall install

clean:
	rm -f azblob-importer azblob-exporter azblob-storage azblob_*.ptar
