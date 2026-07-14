GO      = go
EXT     =

PLAKAR  ?= plakar
VERSION ?= v1.0.0

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := sftp_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o sftpImporter${EXT} ./plugin/importer
	${GO} build -v -o sftpExporter${EXT} ./plugin/exporter
	${GO} build -v -o sftpStorage${EXT} ./plugin/exporter

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm sftp

install: package
	$(PLAKAR) pkg add ./$(PTAR)

reinstall: uninstall install

clean:
	rm -f sftpImporter sftpExporter sftpStorage sftp-*.ptar