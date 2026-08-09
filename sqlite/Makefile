GO=go
EXT=

all: build

build:
	${GO} build -v -o sqliteStorage${EXT} ./plugin/storage

clean:
	rm -f sqliteStorage sqlite-*.ptar
