# STDIO Integration

This integration is bundled in plakar by default and is being used
as a build-time dependency of https://github.com/PlakarKorp/plakar/ 

## Overview

**STDIO (Standard Input/Output)** refers to the standard streams provided by most operating systems for input and output operations. In the context of this integration, STDIO allows you to use standard input, standard output, or standard error as sources or destinations for backup and restore operations.

This integration allows:

- Seamless backup of data from standard input (stdin) into a Kloset repository
- Direct restoration of snapshots to standard output (stdout) or standard error (stderr)
- Easy integration with scripts, pipelines, and tools that use UNIX-style input/output

## Configuration

The configuration parameters are as follow:

- `location` (required):
  - For sources: use `stdio` to read from standard input
  - For destinations: use `stdout` or `stderr` to write to standard output or standard error

> **Note:** The location can be write directly in the command, with `stdio://`, `stdout://` or `stderr://` prefix. No need to add it in the configuration.

## Examples

```bash
# backup the source (e.g., from a file or command output)
$ cat myfile.txt | plakar at /tmp/store backup stdio://

# restore the snapshot (writes to stdout or stderr)
$ plakar at /tmp/store restore -to stdout:// <snapid>
```
