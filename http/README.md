# HTTP(S) Integration

## Overview

**HTTP(S)** is a ubiquitous, protocol-agnostic transport layer for accessing content over the web.
This integration allows a Kloset repository to be backed by a remote HTTP-based object store or content endpoint that exposes read/write operations over standard HTTP(S).

This integration allows:

* **Storing a Kloset repository on an HTTP-accessible storage endpoint:**
  Use any compatible HTTP/HTTPS server or service as a remote backend for Kloset.
  Ideal for custom object stores, static web servers, or self-hosted APIs with PUT/GET semantics.

* **Stateless and protocol-agnostic storage integration:**
  No client-specific agent or protocol extensions are required — repository objects are addressed via standard HTTP methods using content hashes.

* **Compatibility with custom cloud gateways and CDN-backed object layers:**
  Useful in advanced setups involving reverse proxies, serverless gateways, or content distribution networks exposing HTTP-backed object storage.


---

## Configuration

No configuration is exposed for this integration.


---

## Examples

```sh
# create a repository on a remote HTTP server
$ plakar at http://storage.example.com/ create

# back up a local directory to the HTTP-backed repository
$ plakar at http://storage.example.com/ backup /etc

# list snapshots in the repository
$ plakar at http://storage.example.com/ ls
```
