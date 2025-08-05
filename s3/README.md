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

---

## Configuration

The supported configuration options are:

* `access_key_id`: AWS access key ID
* `secret_access_key`: AWS secret access key
* `use_tls` (default: true): disable TLS support, useful for local instances such as Minio.

---

## Examples

```sh
# back up a bucket
$ plakar backup s3://bucket_name

# restore the snapshot "abc" to a bucket
$ plakar restore -to s3://bucket_name abc

# create a kloset repository to store your backups on a bucket
$ plakar at s3://bucket_name create
```
