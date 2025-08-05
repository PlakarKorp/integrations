# STDIO Integration

## What is STDIO?

**STDIO (Standard Input/Output)** refers to the standard streams provided by most operating systems for input and output operations. In the context of this integration, STDIO allows you to use standard input, standard output, or standard error as sources or destinations for backup and restore operations.

This integration allows:

- Seamless backup of data from standard input (stdin) into a Kloset repository
- Direct restoration of snapshots to standard output (stdout) or standard error (stderr)
- Easy integration with scripts, pipelines, and tools that use UNIX-style input/output

## Installation

If a pre-built package exists for your system and architecture,
you can simply install it using:

```sh
$ plakar pkg add stdio
```

Otherwise,
you can first build it:

```sh
$ plakar pkg build stdio
```

This should produce `stdio-vX.Y.Z.ptar` that can be installed with:

```bash
$ plakar pkg add ./stdio-v0.1.0.ptar
```

## Configuration

The configuration parameters are as follow:

- `location` (required):
  - For sources: use `stdio` to read from standard input
  - For destinations: use `stdout` or `stderr` to write to standard output or standard error

## Example Usage

```bash
# configure a STDIO source (reads from stdin)
$ plakar source add mySTDIOsrc stdio

# backup the source (e.g., from a file or command output)
$ cat myfile.txt | plakar backup @mySTDIOsrc

# configure a STDIO destination (writes to stdout)
$ plakar destination add mySTDIOdst stdout

# restore the snapshot to the destination (writes to stdout)
$ plakar restore -to @mySTDIOdst <snapid>

# configure a STDIO destination (writes to stderr)
$ plakar destination add mySTDIOerr stderr

# restore the snapshot to the destination (writes to stderr)
$ plakar restore -to @mySTDIOerr <snapid>
```
