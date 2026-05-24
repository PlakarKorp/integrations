all: build

build:
	go build -v -o test-importer ./importer
	go build -v -o test-exporter ./exporter
	go build -v -o test-storage ./storage

create:
	plakar pkg create manifest.yaml v1.1.0-beta.4

clean:
	rm -f test-importer test-exporter test-storage test-integration_*.ptar

.PHONY: all build create clean
