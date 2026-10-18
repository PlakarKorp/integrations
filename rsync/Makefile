GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v0.0.1-beta.1

all: build

build:
	${GO} build -v -o rsyncImporter${EXT} ./plugin/importer

package: build
	rm -f rsync_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-$(PLAKAR) pkg rm rsync

install: package
	$(PLAKAR) pkg add -allow-unsigned ./rsync_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f rsyncImporter${EXT} rsync_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
