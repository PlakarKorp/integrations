# OCI Registry Store Integration

## Overview

**OCI Registries (Open Container Initiative registries)** are the standard distribution mechanism for container images and related artifacts (Helm charts, SBOMs, signatures, WASM modules, generic OCI artifacts). Popular implementations include Docker Hub, Amazon ECR, Google Artifact Registry, Azure Container Registry, GitHub Container Registry, and self-hosted registries such as Harbor, zot and the reference `registry` image.

This integration allows Kloset / Plakar to use an OCI registry as a **native storage backend** for repositories, leveraging existing registry infrastructure, authentication, replication, and immutability guarantees.

With this store, repository data is encoded as OCI artifacts and pushed to a registry, making it easy to host backups alongside container images while benefiting from cloud-native distribution and access controls.

## Capabilities

* **Store a full Kloset repository in an OCI registry**
  Packfiles, states, locks and the repository configuration are stored as OCI artifacts under a dedicated repository.

* **Content-addressed, immutable storage**
  Every object is one blob, addressed by its SHA-256 digest, under a manifest tagged with the object's name.

* **Registry authentication**
  Basic authentication, static bearer tokens, and the token service flow of Docker Hub, GHCR, ECR, Harbor and the like: the connector answers the registry's challenge, trades the credentials for a scoped token at the advertised realm, and reuses the token until it expires.

* **Registry-native distribution and replication**
  Blob downloads follow the registry's redirects to a CDN or an object store, over TLS, with credentials stripped. Tag listings follow the registry's pagination.

* **Compatible with managed and self-hosted registries**
  Targets the OCI Distribution Specification; works with any compliant registry, including those that only delete manifests by digest.

* **Air-gapped and firewall-friendly deployments**
  Uses standard HTTPS traffic and registry APIs already allowed in most environments.

## Repository Layout

Each repository object is a tagged manifest with a single layer:

* **Tags** name the objects: `CONFIG`, `packfiles-<mac>`, `state-<mac>`, `locks-<mac>` (and `ecc-packfiles-<mac>`, `ecc-state-<mac>` for error-correcting copies)
* **Layers** hold the object's bytes, as `application/octet-stream` blobs
* **Manifests** are `application/vnd.oci.image.manifest.v1+json`, with an empty config blob shared by all of them and an `io.plakar.key` annotation naming the tag, so that two objects with identical bytes never share a manifest

Listing a resource lists the repository's tags; reading an object fetches its manifest, then its layer, with an HTTP `Range` for partial reads; deleting an object deletes its tag, or its manifest by digest on registries that cannot untag.

## Configuration

The configuration parameters are as follows:

* `location` (required): the repository the store lives in, as `oci://<host>[:<port>]/<repository>`
  (e.g. `oci://ghcr.io/my-org/plakar-store`, `oci://localhost:5000/backups`)
* `username`, `password`: credentials for the registry. The password is often a personal access token. They are sent to the registry, and to the token service it names, over TLS.
* `token`: a bearer token sent as is, instead of `username` and `password`
* `insecure`: talk to the registry over plain HTTP (default `false`)
* `tls_insecure_no_verify`: skip the verification of the registry's certificate (default `false`; for testing only)
* `allow_cross_host_uploads`: follow upload locations pointing at another host than the registry (default `false`; see below)

Credentials never go in the location.

## Examples

Start a test registry container with deletes enabled:

```bash
$ docker run -d --name oci-registry \
  -p 5000:5000 \
  -e REGISTRY_STORAGE_DELETE_ENABLED=true \
  -v $(pwd)/registry-data:/var/lib/registry \
  registry:2
```

Set up a store:

```bash
# Configure an OCI registry store
$ plakar store add myregistry oci://localhost:5000/helloworld insecure=true
$ plakar at @myregistry create

# Use the store for backups
$ plakar -silent at @myregistry backup /path/to/data

# List snapshots
$ plakar at @myregistry ls

# Restore a snapshot
$ plakar at @myregistry restore <snapid>
```

On a hosted registry:

```bash
$ plakar store add ghcr oci://ghcr.io/my-org/plakar-store username=my-user password=ghp_...
$ plakar at @ghcr create
```

## Local Testing

`make dev` installs the plugin from this tree, starts a plain HTTP registry on
port 5000 and a self-signed TLS one on port 5443, and prints the
`plakar store add` commands for both. `make down` stops them.

`make test` runs the test suite against an in-memory registry that imitates
the behaviours of real ones (token authentication, paginated listings,
redirected downloads, registries without tag deletion). `make test-live` runs
it against the TLS registry started by `make up` as well, as CI does against
the reference `registry` image. Any registry can be targeted with
`OCI_TEST_REGISTRY=<host>[:<port>]`, plus `OCI_TEST_TLS=1` for TLS and
`OCI_TEST_USERNAME`/`OCI_TEST_PASSWORD` or `OCI_TEST_TOKEN` to authenticate.

## Security

The connector pins where the data and the credentials go:

* Credentials are sent to the registry host only, plus the token service a
  bearer challenge names, and only over HTTPS unless `insecure=true`.
* Blob downloads follow redirects, as registries hand them off to CDNs and
  object stores, but a redirect never downgrades to cleartext and the
  `Authorization` header is dropped when it leaves the registry host.
* Uploads never follow a redirect, and the upload location a registry returns
  must stay on the registry host, unless `allow_cross_host_uploads=true`: the
  upload body goes wherever that location points.
* Manifest digests received from the registry are validated before they are
  used in a request.

## Read Integrity

The connector does not hash blobs read from the registry or compare them to
their digest. This is deliberate, not an oversight:

* The registry serves both the manifest and the blob. Checking the blob against
  the manifest's digest trusts a manifest the same registry controls, so a
  malicious registry can replace both and the check still passes.
* Kloset authenticates data above the storage layer, with a key the registry
  never sees. In encrypted repositories (the default) every buffer is decrypted
  with AES-256-GCM-SIV, so modified bytes fail decryption, and the packfile
  index is checked against the MAC in its footer.
* Most reads are ranged: restore reads entries out of packfiles. A range cannot
  be checked against a whole-blob digest, while Kloset's checks cover ranged
  and whole reads alike.

A digest check in the connector would add a hash over every read without
protecting against anything Kloset does not already catch. Plaintext
repositories have no keyed authentication, and a connector check would not
change that for the same reason.

## Use Cases

* **Cloud-native backup storage** using existing container registries
* **Disaster recovery** with registry replication across regions
* **Backup distribution** through standard OCI tooling
* **Air-gapped environments** where registries are the only allowed artifact store
* **Unified artifact management** (images, SBOMs, backups in one system)

## Notes and Best Practices

* Use **dedicated repositories** for backup data to avoid mixing with container images.
* **Enable deletion** on the registry (`REGISTRY_STORAGE_DELETE_ENABLED=true` on the reference image): Plakar deletes its locks, and maintenance deletes packfiles.
* Run the registry's **garbage collection** now and then: deleting a manifest leaves its blob behind until then.
* Prefer **token-based authentication** over static passwords.
* Monitor registry quotas and storage costs, especially for large repositories.

## Limitations

* Uploads stream with an unknown length (chunked transfer encoding); a registry that insists on a `Content-Length` for uploads is not supported.
* Performance depends on registry implementation and blob size limits.
* Some registries enforce strict rate limits on pushes and pulls.

## Compatibility

This store targets the OCI Distribution Specification and should work with any compliant registry implementation.
