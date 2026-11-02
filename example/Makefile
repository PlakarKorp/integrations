GO	= go
EXT	=

PLAKAR	= plakar
VERSION	?= v1.1.0

# Lint rules and tool versions come from PlakarKorp/QA at this version, as in CI.
QA_VERSION != cat .qa-version
QA_DIR     =  .qa/$(QA_VERSION)

all: build

build:
	${GO} build -v -o example-importer${EXT} ./plugin/importer
	${GO} build -v -o example-exporter${EXT} ./plugin/exporter
	${GO} build -v -o example-storage${EXT} ./plugin/storage

test:
	${GO} vet ./...
	${GO} test -race ./...

check: test

create package: build
	rm -f example_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm example

install: create
	${PLAKAR} pkg add -allow-unsigned ./example_${VERSION}_$$(${GO} env GOOS)_$$(${GO} env GOARCH).ptar

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
	rm -rf example-importer${EXT} example-exporter${EXT} example-storage${EXT} example_*.ptar .qa

.PHONY: all build test check create package uninstall install reinstall lint validate clean
