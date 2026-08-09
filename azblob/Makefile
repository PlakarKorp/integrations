GO	= go
EXT	=

PLAKAR	= plakar
VERSION = v1.1.0

all: build

build:
	${GO} build -v -o azblob-importer${EXT} ./importer
	${GO} build -v -o azblob-exporter${EXT} ./exporter
	${GO} build -v -o azblob-storage${EXT}  ./storage

package: build
	rm -f azblob_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-$(PLAKAR) pkg rm azblob

install: package
	$(PLAKAR) pkg add ./azblob_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f azblob-importer${EXT} azblob-exporter${EXT} azblob-storage${EXT} azblob_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
