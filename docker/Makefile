GO=go

all: build

build:
	${GO} build -v -o dockerImporter ./plugin/importer

clean:
	rm -f dockerImporter docker-*.ptar
