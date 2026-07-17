GO	= go
EXT	=

PLAKAR	= plakar
VERSION	= v1.1.0

all: build

build:
	${GO} build -v -o etcd-importer${EXT} ./cmd/importer

package: build
	rm -f etcd_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm etcd

install: package
	${PLAKAR} pkg add ./etcd_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f etcd-importer${EXT} etcd_${VERSION}*.ptar

.PHONY: all build package uninstall install reinstall test check clean
