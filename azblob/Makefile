GO	= go
EXT	=

PLAKAR	= plakar
VERSION = v1.1.1

# Lint rules and tool versions come from PlakarKorp/QA at this version, as in CI.
QA_VERSION != cat .qa-version
QA_DIR     =  .qa/$(QA_VERSION)

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
	rm -f azblob-importer${EXT} azblob-exporter${EXT} azblob-storage${EXT} azblob_*.ptar
	rm -rf .qa

.PHONY: all build package uninstall install reinstall test check clean lint validate
