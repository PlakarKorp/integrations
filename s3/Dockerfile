FROM scratch

COPY --from=alpine:3.22 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY s3Importer s3Exporter s3Storage /usr/local/bin/

USER 65534:65534

