GO	= go
EXT	=

VERSION ?= v1.0.0

all: build

build:
	${GO} build -v -o example-importer${EXT} ./plugin/importer
	${GO} build -v -o example-exporter${EXT} ./plugin/exporter
	${GO} build -v -o example-storage${EXT} ./plugin/storage

create:
	plakar pkg create manifest.yaml $(VERSION)

install:
	plakar pkg add -allow-unsigned ./example_$(VERSION)_$$(go env GOOS)_$$(go env GOARCH).ptar

clean:
	rm -f example-importer${EXT} example-exporter${EXT} example-storage${EXT} example_*.ptar

.PHONY: all build create install clean
