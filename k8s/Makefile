GO=go
EXT=

all: build

build:
	${GO} build -v -o k8s-importer${EXT} ./importer
	${GO} build -v -o k8s-exporter${EXT} ./exporter

clean:
	rm -f k8s-importer k8s-exporter k8s_*.ptar
