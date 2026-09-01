GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.1.5

all: build

build:
	${GO} build -v -o sftp-importer${EXT} ./cmd/importer
	${GO} build -v -o sftp-exporter${EXT} ./cmd/exporter
	${GO} build -v -o sftp-storage${EXT} ./cmd/storage

package: build
	rm -f sftp_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-$(PLAKAR) pkg rm sftp

install: package
	$(PLAKAR) pkg add -allow-unsigned ./sftp_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f sftp-importer${EXT} sftp-exporter${EXT} sftp-storage${EXT} sftp_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
