GO=go
PLAKAR=plakar

VERSION=v1.1.0-beta.1

# Lint rules and tool versions come from PlakarKorp/QA at this version, as in CI.
QA_VERSION != cat .qa-version
QA_DIR     =  .qa/$(QA_VERSION)

all: build

build: importer exporter

importer:
	${GO} build -v -o openwrt-importer ./plugin/importer

exporter:
	${GO} build -v -o openwrt-exporter ./plugin/exporter

package: build
	rm -f openwrt_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

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
	rm -f openwrt-importer
	rm -f openwrt-exporter
	rm -f fetcharchive
	rm -rf .qa

fclean: clean
	rm -f openwrt_${VERSION}_*.ptar

re: clean build

install: package
	${PLAKAR} pkg add -allow-unsigned ./openwrt_${VERSION}_*.ptar

uninstall:
	${PLAKAR} pkg rm openwrt

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

fetcharchive:
	go build -v -o fetcharchive ./cmd/fetcharchive/

.PHONY: all build importer exporter package clean fclean re install uninstall reinstall test check lint validate
