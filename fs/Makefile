GO  = go
EXT =

PLAKAR  = plakar
VERSION = v1.1.9

# Lint rules and tool versions come from PlakarKorp/QA at this version, as in CI.
QA_VERSION != cat .qa-version
QA_DIR     =  .qa/$(QA_VERSION)

all: build

build:
	${GO} build -v -o fs-importer${EXT} ./plugin/importer
	${GO} build -v -o fs-exporter${EXT} ./plugin/exporter
	${GO} build -v -o fs-storage${EXT}  ./plugin/storage

test:
	${GO} vet ./...
	${GO} test ./...

check: test

package: build
	rm -f fs_*.ptar
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm fs

install: package
	$(PLAKAR) pkg add -allow-unsigned ./fs_${VERSION}_*.ptar

reinstall: uninstall install

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
	rm -f fs-importer fs-exporter fs-storage fs_*.ptar
	rm -rf .qa

.PHONY: all build test check package uninstall install reinstall clean lint validate
