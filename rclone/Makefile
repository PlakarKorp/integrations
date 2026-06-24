GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.0.0-beta.9

all: build

build:
	${GO} build -v -o rclone-importer${EXT} ./plugin/importer
	${GO} build -v -o rclone-exporter${EXT} ./plugin/exporter
	${GO} build -v -o rclone-storage${EXT} ./plugin/storage

package: build
	rm -f rclone_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm rclone

install: package
	${PLAKAR} pkg add ./rclone_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f rclone-importer${EXT} rclone-exporter${EXT} rclone-storage${EXT} rclone_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
