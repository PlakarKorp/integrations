GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.1.0

all: build

build:
	${GO} build -v -o routeros-importer${EXT} ./cmd/importer
	${GO} build -v -o routeros-exporter${EXT} ./cmd/exporter

package: build
	rm -f routeros_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm routeros

install: package
	${PLAKAR} pkg add -allow-unsigned ./routeros_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f routeros-importer${EXT} routeros-exporter${EXT} routeros_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
