GO  = go
EXT =

PLAKAR  = plakar
VERSION = v1.1.8

all: build

build:
	${GO} build -v -o fs-importer${EXT} ./plugin/importer
	${GO} build -v -o fs-exporter${EXT} ./plugin/exporter
	${GO} build -v -o fs-storage${EXT}  ./plugin/storage

test:
	${GO} vet ./...
	${GO} test ./...

check: test

package: build
	rm -f fs_*.ptar
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm fs

install: package
	$(PLAKAR) pkg add -allow-unsigned ./fs_${VERSION}_*.ptar

reinstall: uninstall install

clean:
	rm -f fs-importer fs-exporter fs-storage fs_*.ptar

.PHONY: all build test check package uninstall install reinstall clean
