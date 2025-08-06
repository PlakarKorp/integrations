# IMAP integration

## Overview

IMAP (Internet Message Access Protocol) is a standard email protocol used by mail clients to retrieve messages from a mail server over a TCP/IP connection.
It is widely adopted for managing and accessing email stored on remote servers.

This integration allows:

- **Seamless backup of emails hosted on IMAP servers into a Kloset repository:**
Capture and store entire mailbox contents (including folders, metadata, and attachments) for long-term archival and audit purposes.

- **Direct restoration of mailbox snapshots to IMAP servers:**
Restore previously backed-up snapshots directly to any compatible IMAP destination, maintaining folder structure and message integrity.

- **Compatibility with legacy and modern email systems using IMAP:**
Ensures support for a wide range of mail providers and servers, including self-hosted and enterprise environments.

## Installation

To install the IMAP integration, you can use the following command:

```sh
$ plakar pkg add imap
```

You can check if the installation was successful by running:

```sh
$ plakar pkg
```

## Configuration

The configuration parameters are as follow:

- `location`: The URL of the IMAP server in the form imap://<host>:<port>.
- `username`: Username to login.
- `password`: Password for login.
- `tls`:      TlS mode to use.  Possible values are tls (the default), starttls and no-tls.
- `tls_no_verify`: If set to yes, the client will not verify the server certificate in tls mode.

## Examples

```bash
# configure an IMAP source connector
$ plakar source add myIMAPsrc imap://imap.mydomain.com:143 \
    username=myuser     \
    password=mypassword \
    tls=starttls

# backup the mailbox
$ plakar backup @myIMAPsrc

# configure an IMAP destination connector
$ plakar destination add myIMAPdst imap://imap.alsomydomain.com:143 username=alsomyuser password=alsomypassword tls=starttls

# restore the snapshot to the destination
$ plakar restore -to @myIMAPdst <snapid>
```
