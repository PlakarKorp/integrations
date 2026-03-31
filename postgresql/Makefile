GO      = go
EXT     =

PLAKAR  ?= plakar
VERSION ?= v1.0.0

GOOS   := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
PTAR   := postgresql_$(VERSION)_$(GOOS)_$(GOARCH).ptar

all: build

build:
	${GO} build -v -o postgresqlImporter${EXT} ./plugin/importer
	${GO} build -v -o postgresqlExporter${EXT} ./plugin/exporter
	${GO} build -v -o postgresqlBinImporter${EXT} ./plugin/binimporter

package: build
	$(PLAKAR) pkg create ./manifest.yaml $(VERSION)

uninstall:
	-$(PLAKAR) pkg rm postgresql

install: package
	$(PLAKAR) pkg add ./$(PTAR)

reinstall: uninstall install

# Start a throw-away PostgreSQL instance for restore testing.
# The restore hint is printed first since docker run blocks.
testdb:
	@echo "To restore a snapshot to this database:"
	@echo "  $(PLAKAR) destination rm mydb"
	@echo "  $(PLAKAR) destination add mydb postgres://postgres@localhost:9999 password=postgres"
	@echo "  $(PLAKAR) restore -to @mydb <snapid>"
	@echo ""
	docker run --rm -ti --name test -p 9999:5432 -e POSTGRES_PASSWORD=postgres postgres

clean:
	rm -f postgresqlImporter postgresqlExporter postgresqlBinImporter
