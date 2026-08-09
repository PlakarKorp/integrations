# GCS Integration

## Overview

**Google Cloud Storage** is a scalable, secure, and durable object storage service offered by Google Cloud Platform (GCP).
It is commonly used for storing and retrieving large volumes of unstructured data such as backups, logs, media, and datasets.

This integration allows:

* **Seamless backup of buckets and objects into a Kloset repository:**
  Capture and store entire GCS buckets or specific object paths with full metadata preservation, enabling consistent backup of cloud-native assets.

* **Direct restoration of snapshots to Google Cloud Storage:**
  Restore previously backed-up snapshots directly into GCS buckets, maintaining original object hierarchies and metadata such as custom attributes, content-type, and ACLs.

* **Compatibility with modern cloud-native workflows and tools:**
  Integrates with GCP environments, CI/CD pipelines, and hybrid cloud architectures, supporting use cases like disaster recovery, data migration, and cloud-to-cloud backup.


## Configuration

If possible, the integration will auto-authenticate itself, for e.g. if
you have the gcloud cli installed locally.  Otherwise, a service
account file can be explicitly provided.

The supported configuration options are:

- `credentials_file`: path to a JSON credentials file
- `credentials_json`: an in-line credentials JSON
- `endpoint`: to override the endpoint used, mostly for debug
- `no_auth`: to avoid authentication, useful to backup a public bucket
  for example


## Examples

```sh
# back up a bucket
$ plakar at /tmp/store backup gcs://bucket_name

# restore the snapshot "abc" to a bucket
$ plakar at /tmp/store restore -to gcs://bucket_name abc

# create a kloset repository to store your backups on a bucket
$ plakar at gs://bucket_name create
```
