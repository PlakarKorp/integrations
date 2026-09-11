GO	= go
EXT	=

PLAKAR	= plakar
DOCKER	= docker
VERSION	= v1.1.9

all: build

build:
	${GO} build -v -o k8s-importer${EXT} ./plugin/importer
	${GO} build -v -o k8s-exporter${EXT} ./plugin/exporter
	${GO} build -v -o k8s-inventory${EXT} ./plugin/inventory

build-kubelet:
	${DOCKER} build -f kubelet/Dockerfile -t kubelet .

package: build
	rm -f k8s_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm k8s

install: package
	${PLAKAR} pkg add --allow-unsigned ./k8s_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

clean:
	rm -f k8s-importer${EXT} k8s-exporter${EXT} k8s-inventory${EXT} k8s_*.ptar

.PHONY: all build build-kubelet package uninstall install reinstall test check clean
