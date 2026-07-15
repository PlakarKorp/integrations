# Azure Blob Integration

## Overview

**Azure Blob Storage** is a scalable, secure, and durable object storage service offered by Microsoft Azure.
It is designed for storing large amounts of unstructured data such as backups, logs, media, and datasets.

This integration allows:

* **Seamless backup of containers and blobs into a Kloset repository:**
  Capture and store entire Azure Blob containers or specific prefixes with metadata preservation, enabling consistent backup of cloud-native assets.

* **Direct restoration of snapshots to Azure Blob Storage:**
  Restore previously backed-up snapshots directly into containers, maintaining original blob hierarchies and properties such as content type and timestamps.

* **Compatibility with modern cloud-native workflows and tools:**
  Integrates with Azure environments, CI/CD pipelines, and hybrid cloud architectures, supporting use cases like disaster recovery, data migration, and cloud-to-cloud backup.

---

## Configuration

Authentication can be provided in multiple ways depending on your environment.

The supported configuration options are:

* `connection_string`: Azure Storage connection string (recommended)
* `account_name`: Azure Storage account name
* `account_key`: Azure Storage account key
* `endpoint`: custom endpoint (e.g. Azurite or non-standard Azure environments)
* `no_auth`: disable authentication (useful for public containers or testing)
* `use_managed_identity`: enable Azure AD authentication through `DefaultAzureCredential`
* `managed_identity_client_id`: optional client ID for user-assigned managed identity

At least one authentication method must be provided:

* `connection_string`
* OR `account_name` + `account_key`
* OR `no_auth=true` with a valid `endpoint`
* OR `use_managed_identity=true` with:
  * `account_name` (service URL inferred as `https://<account_name>.blob.core.windows.net`)
  * or `endpoint`

Managed identity notes:

* System-assigned identity: set `use_managed_identity=true`.
* User-assigned identity: set `use_managed_identity=true` and `managed_identity_client_id=<client-id>`.

---

## Examples

```sh
# back up a container
$ plakar at /tmp/store backup azblob://container_name

# back up a specific prefix
$ plakar at /tmp/store backup azblob://container_name/path

# restore the snapshot "abc" to a container
$ plakar at /tmp/store restore -to azblob://container_name abc

# create a kloset repository on a container
$ plakar at azblob://container_name create
```

---

## Notes

* Azure Blob Storage uses **containers** instead of buckets.
* Blob storage is **flat**, directory structures are simulated using prefixes.
* Empty directories are not preserved unless they contain blobs.
* When using **Azurite** (local emulator), you typically need:

  * a connection string
  * or `account_name=devstoreaccount1` with a local endpoint
