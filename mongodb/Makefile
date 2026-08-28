GO = go
EXT=

V=v1.1.0-devel.0

all: build

build:
	${GO} build -v -o mongodb-importer${EXT} ./importer
	${GO} build -v -o mongodb-exporter${EXT} ./exporter

clean:
	rm -f mongodb-importer${EXT}
	rm -f mongodb-exporter${EXT}
	rm -f ./mongodb_${V}*.ptar

package:
	rm -f ./mongodb_${V}*.ptar
	plakar pkg create manifest.yaml ${V}

install: package
	plakar pkg rm mongodb
	plakar pkg add ./mongodb_${V}*.ptar
