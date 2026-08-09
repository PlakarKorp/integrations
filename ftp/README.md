# FTP Integration

## Overview

**FTP (File Transfer Protocol)** is a standard network protocol used to transfer files between a client and server over a TCP/IP connection. It’s widely used for accessing and managing files on remote servers.

This integrations allows:

- **Seamless backup of files hosted on FTP servers into a Kloset repository:**
Capture and store entire directories over FTP.
- **Direct restoration of snapshots to remote FTP destinations:**
Restore previously backed-up snapshots directly to any compatible FTP destination, maintaining folder structure and message integrity.
- **Compatibility with legacy systems and tools that use FTP:**
Ensures support for a wide range of FTP providers and servers, including self-hosted and enterprise environments.


## Configuration

The configuration parameters are as follow:

- `location` (required): The URL of the FTP server in the form ftp://&lt;host&gt;[:&lt;port&gt;]
- `username` (optional): The username to authenticate as (defaults to anonymous)
- `password` (optional): The password to authenticate with (defaults to anonymous)


## Examples

```bash
# configure an FTP source
$ plakar source add myFTPsrc ftp://ftp.example.org/pub/somedirectory

# backup the source
$ plakar backup @myFTPsrc

# configure an FTP destination
$ plakar destination add myFTPdst ftp://ftp.example.org/upload

# restore the snapshot to the destination
$ plakar restore -to @myFTPdst <snapid>
```
