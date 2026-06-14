GO	= go
EXT	=

all: build

build:
	${GO} build -v -o test-importer${EXT} ./importer
	${GO} build -v -o test-exporter${EXT} ./exporter
	${GO} build -v -o test-storage${EXT} ./storage

create:
	plakar pkg create manifest.yaml v1.1.0-beta.4

clean:
	rm -f test-importer${EXT} test-exporter${EXT} test-storage${EXT} test-integration_*.ptar

.PHONY: all build create clean
