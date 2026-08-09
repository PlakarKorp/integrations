# gRPC Integration

## Overview

If you're looking for how to write [Plakar][plakar] integrations,
please take a look at the [SDK][sdk] instead.  It only exists to
provide the Plakar-side bits to talk with plugins.

[plakar]: https://github.com/PlakarKorp/plakar
[sdk]:    https://github.com/PlakarKorp/go-kloset-sdk

---

This integration exists to wrap a client talking our GRPC protocol as
a Kloset exporter, importer or storage.  It also includes the protobuf
declarations needed to write integrations in other languages for which
we don't provide yet an official SDK.


## Configuration

The gRPC integration does not require any specific configuration
parameters; it merely proxies the one given.
