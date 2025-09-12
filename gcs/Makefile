GO = go
EXT=

all: build

build:
	${GO} build -v -o gcs-importer${EXT} ./importer
	${GO} build -v -o gcs-exporter${EXT} ./exporter
	${GO} build -v -o gcs-storage${EXT}  ./storage

clean:
	rm -f gcs-importer gcs-exporter gcs-storage
