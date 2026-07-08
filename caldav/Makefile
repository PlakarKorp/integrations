GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.0.0-beta.8

all: build

build:
	${GO} build -v -o caldav-importer${EXT} ./cmd/importer
	${GO} build -v -o caldav-exporter${EXT} ./cmd/exporter

package: build
	rm -f caldav_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm caldav

install: package
	${PLAKAR} pkg add ./caldav_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f caldav-importer${EXT} caldav-exporter${EXT} caldav_${VERSION}_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
