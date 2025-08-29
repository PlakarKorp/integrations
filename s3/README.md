# S3 Integration

## Overview

**Amazon S3 (Simple Storage Service)** is a widely-used, scalable, and durable object storage service provided by Amazon Web Services (AWS).
It is ideal for storing and retrieving large volumes of unstructured data such as backups, media, datasets, and logs.

This integration allows:

* **Seamless backup of buckets and objects into a Kloset repository:**
  Capture and store entire S3 buckets or specific object prefixes with full metadata preservation, enabling consistent backup of cloud-native assets.

* **Direct restoration of snapshots to Amazon S3:**
  Restore previously backed-up snapshots directly into S3 buckets, maintaining original object hierarchies and metadata such as tags, content-type, and storage class.

* **Compatibility with modern cloud-native workflows and tools:**
  Integrates with AWS environments, serverless pipelines, and hybrid cloud architectures, supporting use cases like disaster recovery, data archiving, and cross-region backups.

## Configuration

The configuration parameters are as follows:

- `location` (required): The path to the location in the bucket where data will be taken, restored, or stored (e.g., `s3://my-bucket/path/to/data`)
- `access_key` (required): AWS access key ID
- `use_tls` (optional): Whether to use TLS for secure connections (defaults to `true`)
- `tls_insecure_no_verify` (optional): If set to `true`, disables certificate verification (defaults to `false`)
- `secret_access_key` (required): AWS secret access key

For S3-compatible storage providers, you may also need to specify:
- `storage_class`: The storage class to use (e.g., `STANDARD`, `GLACIER`)

## Examples

```bash
# Configure an S3 source
$ plakar source add myS3src s3://s3.us-east-1.amazonaws.com/mybucket access_key=YOUR_ACCESS_KEY secret_access_key=YOUR_SECRET_KEY use_tls=true

# Backup the source
$ plakar at /tmp/example backup @myS3src

# Configure an S3 destination
$ plakar destination add myS3dst s3://s3.us-east-1.amazonaws.com/mybucketdst access_key=YOUR_ACCESS_KEY secret_access_key=YOUR_SECRET_KEY use_tls=true

# Restore the snapshot to the destination
$ plakar at /tmp/example restore -to @myS3dst <snapid>

# Configure an S3 store
$ plakar store add myS3store s3://s3.us-east-1.amazonaws.com/mystore access_key=YOUR_ACCESS_KEY secret_access_key=YOUR_SECRET_KEY use_tls=true storage_class=STANDARD

# Create the store
$ plakar at @myS3store create
``` 
