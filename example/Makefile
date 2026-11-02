VERSION ?= v1.0.0

# Lint rules and tool versions come from PlakarKorp/QA at this version, as in CI.
QA_VERSION != cat .qa-version
QA_DIR     =  .qa/$(QA_VERSION)

all: build

build:
	go build -v -o example-importer ./plugin/importer
	go build -v -o example-exporter ./plugin/exporter
	go build -v -o example-storage ./plugin/storage

test:
	go vet ./...
	go test -race ./...

create:
	plakar pkg create manifest.yaml $(VERSION)

install:
	plakar pkg add -allow-unsigned ./example_$(VERSION)_$$(go env GOOS)_$$(go env GOARCH).ptar

lint: $(QA_DIR)/golangci.yml $(QA_DIR)/versions
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@`sed -n 's/^golangci-lint=//p' $(QA_DIR)/versions` \
		run --config $(QA_DIR)/golangci.yml ./...

validate: $(QA_DIR)/versions
	go run github.com/PlakarKorp/pkg/cmd/validator@`sed -n 's/^validator=//p' $(QA_DIR)/versions` \
		-v repo .

$(QA_DIR)/golangci.yml $(QA_DIR)/versions:
	mkdir -p $(@D)
	curl -fsSL -o $@ https://raw.githubusercontent.com/PlakarKorp/QA/$(QA_VERSION)/integrations/$(@F)

clean:
	rm -rf example-importer example-exporter example-storage example_*.ptar .qa

.PHONY: all build test create install lint validate clean
