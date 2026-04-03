GO = go
EXT=

all: build

build:
	${GO} build -v -o azblob-importer${EXT} ./importer
	${GO} build -v -o azblob-exporter${EXT} ./exporter
	${GO} build -v -o azblob-storage${EXT}  ./storage

clean:
	rm -f azblob-importer azblob-exporter azblob-storage
