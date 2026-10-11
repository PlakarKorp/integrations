VERSION ?= v1.0.0

all: build

build:
	go build -v -o example-importer ./plugin/importer
	go build -v -o example-exporter ./plugin/exporter
	go build -v -o example-storage ./plugin/storage

create:
	plakar pkg create manifest.yaml $(VERSION)

install:
	plakar pkg add -allow-unsigned ./example_$(VERSION)_$$(go env GOOS)_$$(go env GOARCH).ptar

clean:
	rm -f example-importer example-exporter example-storage example_*.ptar

.PHONY: all build create install clean
