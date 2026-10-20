GO      = go
EXT     =

PLAKAR  ?= plakar
VERSION ?= v1.1.3

# Lint rules and tool versions come from PlakarKorp/QA at this version, as in CI.
QA_VERSION != cat .qa-version
QA_DIR     =  .qa/$(QA_VERSION)

GOOS   != go env GOOS
GOARCH != go env GOARCH
PTAR   =  openstack_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o openstack-inventory${EXT} ./plugin/inventory
	${GO} build -v -o openstack-block-importer${EXT} ./plugin/blockImporter
	${GO} build -v -o openstack-block-exporter${EXT} ./plugin/blockExporter
	${GO} build -v -o openstack-instance-importer${EXT} ./plugin/instanceImporter
	${GO} build -v -o openstack-instance-exporter${EXT} ./plugin/instanceExporter

test:
	${GO} vet ./...
	${GO} test -race ./...

package: build
	rm -f $(PTAR)
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm openstack

install: package
	$(PLAKAR) pkg add ./$(PTAR)

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
	rm -rf openstack-inventory openstack-block-importer openstack-block-exporter openstack-instance-importer openstack-instance-exporter *.ptar .qa

.PHONY: all build test package uninstall install reinstall lint validate clean
