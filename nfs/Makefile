GO      = go
EXT     =

PLAKAR  ?= plakar
VERSION ?= v1.1.1

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := nfs_$(VERSION)_$(GOOS)_$(GOARCH).ptar

# Lint rules and tool versions come from PlakarKorp/QA at this version, as in CI.
QA_VERSION != cat .qa-version
QA_DIR     =  .qa/$(QA_VERSION)

all: build

build:
	${GO} build -v -o nfsImporter${EXT} ./plugin/importer
	${GO} build -v -o nfsExporter${EXT} ./plugin/exporter
	${GO} build -v -o nfsStorage${EXT} ./plugin/storage

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm nfs

install: package
	$(PLAKAR) pkg add -allow-unsigned ./$(PTAR)

reinstall: uninstall install

test:
	${GO} test -v ./...

lint: $(QA_DIR)/golangci.yml $(QA_DIR)/versions
	${GO} run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@`sed -n 's/^golangci-lint=//p' $(QA_DIR)/versions` \
		run --config $(QA_DIR)/golangci.yml ./...

validate: $(QA_DIR)/versions
	${GO} run github.com/PlakarKorp/pkg/cmd/validator@`sed -n 's/^validator=//p' $(QA_DIR)/versions` \
		-v repo .

$(QA_DIR)/golangci.yml $(QA_DIR)/versions:
	mkdir -p $(@D)
	curl -fsSL -o $@ https://raw.githubusercontent.com/PlakarKorp/QA/$(QA_VERSION)/integrations/$(@F)

clean:
	rm -f nfsImporter nfsExporter nfsStorage nfs_*.ptar nfs-*.ptar
	rm -rf .qa

.PHONY: all build package uninstall install reinstall test lint validate clean
