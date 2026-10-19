GO  = go
EXT =

PLAKAR  = plakar
VERSION = v1.1.0

all: build

build:
	${GO} build -v -o notion-importer${EXT} ./plugin/importer
	${GO} build -v -o notion-exporter${EXT} ./plugin/exporter

test:
	${GO} vet ./...
	${GO} test -race ./...

check: test

package: build
	rm -f notion_*.ptar
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm notion

install: package
	$(PLAKAR) pkg add -allow-unsigned ./notion_${VERSION}_*.ptar

reinstall: uninstall install

clean:
	rm -f notion-importer notion-exporter notion_*.ptar

.PHONY: all build test check package uninstall install reinstall clean
