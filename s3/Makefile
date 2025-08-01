GO=go

all: build

build:
	${GO} build -v -o s3Importer ./plugin/importer
	${GO} build -v -o s3Exporter ./plugin/exporter
	${GO} build -v -o s3Storage ./plugin/exporter

clean:
	rm -f s3Importer s3Exporter s3Storage s3-*.ptar
