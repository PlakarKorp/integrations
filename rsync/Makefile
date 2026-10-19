GO	= go
EXT	=

PLAKAR	?= plakar
VERSION	?= v0.0.1-beta.1

GOOS	:= $(shell ${GO} env GOOS)
GOARCH	:= $(shell ${GO} env GOARCH)
PTAR	:= rsync_${VERSION}_${GOOS}_${GOARCH}.ptar

all: build

build:
	${GO} build -v -o rsyncImporter${EXT} ./plugin/importer
	${GO} build -v -o rsyncExporter${EXT} ./plugin/exporter

package: build
	rm -f ${PTAR}
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-$(PLAKAR) pkg rm rsync

install: package
	$(PLAKAR) pkg add -allow-unsigned ./${PTAR}

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f rsyncImporter${EXT} rsyncExporter${EXT} rsync_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
