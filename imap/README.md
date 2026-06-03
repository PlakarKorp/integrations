# IMAP integration

## Overview

**IMAP (Internet Message Access Protocol)** is a standard email protocol used by mail clients to retrieve messages from a mail server over a TCP/IP connection.
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

- `location`: The URL of the IMAP server in the form `imap://<host>[:<port>]`. When the port is omitted it defaults to 993 for `tls` and 143 otherwise. Credentials may also be embedded as `imap://user:password@host`.
- `username`: Username to login.
- `password`: Password for login.
- `tls`:      TLS mode to use. Possible values are `starttls` (the default), `tls` and `no-tls`.
- `tls_no_verify`: If set to `true`, the client will not verify the server certificate (dangerous; testing only).
- `io_timeout`: Idle timeout for each IMAP socket operation, as a Go duration (e.g. `2m`, `30s`); default `2m`. If a server stalls mid-response (common with throttling providers), the affected operation is aborted after this timeout and recorded as an error, rather than hanging the whole backup indefinitely.

## Behavior

- **Folder hierarchy** is preserved across servers even when source and
  destination use different hierarchy delimiters (e.g. Dovecot's `.` vs `/`).
  Each mailbox segment is percent-encoded into the snapshot path, so a folder
  named `Work/Notes` or `Reçus` round-trips intact.
- **Message flags** (`\Seen`, `\Answered`, `\Flagged`, `\Draft`, `\Deleted`,
  and keywords such as `$Junk`) are encoded into each message's file name and
  re-applied on restore. The session-only `\Recent` flag is intentionally
  dropped. Messages are fetched with `BODY.PEEK`, so a backup never alters the
  source mailbox.
- On restore, missing mailboxes (and their parents) are created automatically.

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

## Using IMAP as a repository (storage)

In addition to importing and exporting mail, this integration can use an IMAP
account as a **kloset repository** — storing arbitrary backups *inside a
mailbox*. Each repository resource (packfiles, states, locks, config) becomes a
mailbox under a configurable root, and every blob is stored as one e-mail
message: the content MAC goes into an `X-Plakar-Mac` header and the bytes are
base64-encoded into the body.

The store is selected by an `imap://` repository location and accepts the same
connection parameters as the importer/exporter (`username`, `password`, `tls`,
`tls_no_verify`) plus a `root` parameter. Point `plakar create` at an
`imap://<host>` location with those parameters to initialize the repository,
then `backup`/`restore` against it as with any other repository.

Additional storage parameter:

- `root`: root mailbox under which the repository's resource mailboxes are
  created (default `Plakar`).

**Caveats.** This is a deliberate (and fun) abuse of IMAP and inherits the
protocol's limits. Bodies are base64-encoded (+33% size), and most servers cap
message size — very large packfiles may be rejected (chunking blobs across
multiple messages is not yet implemented). IMAP offers no atomic
create-if-absent, so repository locking is best-effort; avoid concurrent writers
to the same repository. Every operation is a round-trip, so it is far slower
than the `fs`, `s3` or `sftp` stores. Verified end-to-end against Dovecot.
