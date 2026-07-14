GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.1.0

all: build

build:
	${GO} build -v -o sftpImporter${EXT} ./plugin/importer
	${GO} build -v -o sftpExporter${EXT} ./plugin/exporter
	${GO} build -v -o sftpStorage${EXT} ./plugin/storage

package: build
	rm -f sftp_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-$(PLAKAR) pkg rm sftp

install: package
	$(PLAKAR) pkg add ./sftp_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f sftpImporter${EXT} sftpExporter${EXT} sftpStorage${EXT} sftp_*.ptar

.PHONY: all build package uninstall install reinstall test check clean
