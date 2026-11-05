GO	= go
EXT	=

PLAKAR	= plakar
DOCKER	= docker
VERSION	= v1.1.13

# Lint rules and tool versions come from PlakarKorp/QA at this version, as in CI.
QA_VERSION != cat .qa-version
QA_DIR     =  .qa/$(QA_VERSION)

all: build

build:
	${GO} build -v -o k8s-importer${EXT} ./plugin/importer
	${GO} build -v -o k8s-exporter${EXT} ./plugin/exporter
	${GO} build -v -o k8s-inventory${EXT} ./plugin/inventory

build-kubelet:
	${DOCKER} build -f kubelet/Dockerfile -t kubelet .

package: build
	rm -f k8s_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm k8s

install: package
	${PLAKAR} pkg add --allow-unsigned ./k8s_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./... -race

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
	rm -f k8s-importer${EXT} k8s-exporter${EXT} k8s-inventory${EXT} k8s_*.ptar
	rm -rf .qa

.PHONY: all build build-kubelet package uninstall install reinstall test check clean lint validate
