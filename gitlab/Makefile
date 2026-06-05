GO      = go
EXT     =

PLAKAR  ?= plakar
VERSION ?= v1.0.0

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := gitlab_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o gitlabImporter${EXT} ./importer
	${GO} build -v -o gitlabExporter${EXT} ./exporter

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm gitlab

install: package
	$(PLAKAR) pkg add ./$(PTAR)

reinstall: uninstall install

test:
	${GO} test ./...

clean:
	rm -f gitlabImporter gitlabExporter gitlab_*.ptar
