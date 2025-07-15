# IMAP integration

## Overview

The **Plakar IMAP integration** enables seamless backup and restoration of IMAP mail folders to and from a [Kloset repository](/posts/2025-04-29/kloset-the-immutable-data-store/).

## Installation

First, build the binaries:

```bash
$ go build -o imapImporter ./plugin/importer
$ go build -o imapExporter ./plugin/exporter
```

Create the ptar plugin:

```bash
$ plakar pkg create manifest.yaml
```

This should produce `imap-vX.Y.Z.ptar` that can be installed with:

```bash
$ plakar pkg add imap-v0.0.1.ptar
```

## Configuration

The configuration parameters are as follow:

- `location`: The URL of the IMAP server in the form imap://<host>:<port>.
- `username`: Username to login.
- `password`: Password for login.
- `tls`:      TlS mode to use.  Possible values are tls (the default), starttls and no-tls.
- `tls_no_verify`: If set to yes, the client will not verify the server certificate in tls mode.

## Example Usage

```bash
# configure an IMAP source connector
$ plakar source add myIMAPsrc imap://imap.mydomain.com:143 username=myuser password=mypassword tls=starttls

# backup the mailbox
$ plakar backup @myIMAPsrc

# configure an IMAP destination connector
$ plakar destination add myIMAPdst imap://imap.alsomydomain.com:143 username=alsomyuser password=alsomypassword tls=starttls

# restore the snapshot to the destination
$ plakar restore -to @myIMAPdst <snapid>
```

## Questions, Feedback, and Support

Found a bug? [Open an issue on GitHub](https://github.com/PlakarKorp/plakar/issues/new?title=Bug%20report%20on%20Filesystem%20integration&body=Please%20provide%20a%20detailed%20description%20of%20the%20issue.%0A%0A**Plakar%20version**)

Join our [Discord community](https://discord.gg/uuegtnF2Q5) for real-time help and discussions.
