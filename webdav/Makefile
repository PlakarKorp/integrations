GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.1.0-alpha.2

all: build

build:
	${GO} build -v -o webdav-importer${EXT} ./plugin/importer
	${GO} build -v -o webdav-exporter${EXT} ./plugin/exporter

package: build
	rm -f webdav_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm webdav

install: package
	${PLAKAR} pkg add ./webdav_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f webdav-importer${EXT} webdav-exporter${EXT} webdav_${VERSION}_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
