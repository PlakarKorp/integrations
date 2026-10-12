GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.1.0

all: build

build:
	${GO} build -v -o example-importer${EXT} ./cmd/importer
	${GO} build -v -o example-exporter${EXT} ./cmd/exporter
	${GO} build -v -o example-storage${EXT} ./cmd/storage

package: build
	rm -f example_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm example

install: package
	${PLAKAR} pkg add ./example_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f example-importer${EXT} example-exporter${EXT} example-storage${EXT} example_${VERSION}*.ptar

.PHONY: all build package uninstall install reinstall test check clean
