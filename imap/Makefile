GO = go

all:
	${GO} build -v -o imapImporter ./plugin/importer
	${GO} build -v -o imapExporter ./plugin/exporter

install: clean all
	../plakar/plakar pkg create manifest.yaml
	../plakar/plakar pkg uninstall imap-v1.0.0.ptar
	../plakar/plakar pkg install imap-v1.0.0.ptar

clean:
	rm -f imapImporter imapExporter imap-v*.ptar
