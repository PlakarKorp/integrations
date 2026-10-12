GO	= go
EXT	=

PLAKAR	= plakar
COMPOSE	= docker compose -f testing/compose.yml
VERSION	= v0.0.1
CMDS = ociStorage

all: build

build:
	@for CMD in ${CMDS}; do 				\
		${GO} build -v -o bin/$${CMD}${EXT} ./cmd/$${CMD};	\
	done

package: build
	rm -f oci_${VERSION}_*.ptar
	${PLAKAR} pkg create ./manifest.yaml ${VERSION}

uninstall:
	-${PLAKAR} pkg rm oci

install: package
	${PLAKAR} pkg add -allow-unsigned ./oci_${VERSION}_*.ptar

reinstall: uninstall install

test:
	${GO} test -v ./...

check: test

up:
	${COMPOSE} up -d

down:
	${COMPOSE} down -v

dev: reinstall up
	@echo
	@echo 'Registries are up. Create a store with one of:'
	@echo '  ${PLAKAR} store add oci-http oci://localhost:5000/plakar insecure=true'
	@echo '  ${PLAKAR} store add oci-tls oci://localhost:5443/plakar tls_insecure_no_verify=true'
	@echo 'then: ${PLAKAR} at @oci-(http|tls) create'

clean:
	rm -f bin/* *.ptar

.PHONY: all build package uninstall install reinstall test check up down dev clean
