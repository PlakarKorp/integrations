# Notion Integration

## Overview

**Notion** is a collaborative workspace platform for notes, docs, databases, and project management. This integration allows you to back up and restore Notion pages and workspaces using Plakar.

This integration allows:

- Seamless backup of Notion pages or entire workspaces into a Plakar repository
- Direct restoration of snapshots to Notion (where permitted by the Notion API)
- Structured storage of Notion data as JSON, including metadata and block structure

## Installation

If a pre-built package exists for your system and architecture,
you can simply install it using:

```sh
$ plakar pkg add notion
```

Otherwise,
you can first build it:

```sh
$ plakar pkg build notion
```

This should produce `notion-vX.Y.Z.ptar` that can be installed with:

```bash
$ plakar pkg add ./notion-v0.1.0.ptar
```

## Configuration

The configuration parameters are as follow:

- `token` (required): Your Notion API integration token (e.g., ntn_xxx)
- `rootID` (optional for restore): The Notion page ID to restore content to

## Examples

```bash
# configure a Notion source
$ plakar source add myNotionSrc notion:// token=<ntn_xxx>

# backup the source
$ plakar at /tmp/store backup @myNotionSrc

# configure a Notion destination (for restore)
$ plakar destination add myNotionDst notion:// token=<ntn_xxx> rootID=<root_page_id>

# restore the snapshot to the destination
$ plakar at /tmp/store restore -to @myNotionDst <snapid>
```

Replace `<ntn_xxx>` with your actual Notion API token and `<root_page_id>` with the target Notion page ID.

## Notes

- Make sure your Notion integration is shared with the pages you want to back up or restore.
- Media files (images, videos, etc.) may not be fully supported due to Notion API limitations.
- Keep your API token secure.
