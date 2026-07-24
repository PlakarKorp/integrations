GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.1.0

all: build

build:
	${GO} build -v -o ftpImporter${EXT} ./plugin/importer
	${GO} build -v -o ftpExporter${EXT} ./plugin/exporter

package: build
	rm -f ftp_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm ftp

install: package
	${PLAKAR} pkg add ./ftp_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f ftpImporter${EXT} ftpExporter${EXT} ftpIntegration_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
