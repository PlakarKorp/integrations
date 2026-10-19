# Notion integration

Backs up the pages and databases a Notion workspace shares with an
integration, and restores them under a page of a workspace.

## Installation

```sh
$ plakar pkg add notion
```

Or build it from this directory:

```sh
$ make package
$ plakar pkg add -allow-unsigned ./notion_v1.1.0_*.ptar
```

## Setup

1. Create an internal integration at <https://www.notion.so/profile/integrations>
   and copy its secret (`ntn_...`). The backup needs the *Read content*
   capability; the restore also needs *Insert content*.
2. Open each top-level page or database to back up, choose *Connections*
   in the `...` menu, and add the integration. Everything under a shared
   page is shared with it.

## Configuration

Both connectors take:

- `location`: `notion://` backs up everything shared with the integration,
  `notion://<id>` one page or database and what is under it. The exporter
  needs the id of the page to restore under: the API cannot create pages
  at the top of a workspace. A pasted Notion URL works in place of the id.
- `token`: the integration secret.
- `skip_attachments` (optional): leave out the files Notion hosts, images,
  videos, PDFs, icons and covers.

```sh
$ plakar source add notes notion:// token=ntn_xxx
$ plakar at /var/backups/plakar backup @notes

$ plakar destination add notes-restore notion://<page-id> token=ntn_xxx
$ plakar at /var/backups/plakar restore -to @notes-restore <snapshot>
```

A subtree restores the same way, `restore -to @notes-restore <snapshot>:/pages/<id>`.

## What a snapshot holds

```
/workspace.json                 the roots of the backup, with their titles
/pages/<id>/page.json           the page as the API returns it
/pages/<id>/blocks.json         its blocks, children nested under "children"
/pages/<id>/files/<block-id>.*  the files Notion hosted for its blocks
/pages/<id>/files/icon.*        a hosted icon, likewise cover.*
/pages/<id>/pages/<id>/...      child pages
/pages/<id>/databases/<id>/...  child databases
/databases/<id>/database.json   the database, schema included
/databases/<id>/pages/<id>/...  its rows, each a page
```

Directories are named by id rather than title, so a renamed page stays
the same page to the deduplicating store. Objects are stored byte for
byte as Notion served them, which keeps the backup usable with other
tools and lets a later version of this integration restore more of it.

## What a restore does

Every restore creates new pages: the API has no way to put a page back
under its old id. Under the target page come the roots of the backup, in
order; blocks are appended in their original order, in batches of a
hundred; child pages and databases go where their block was. Links and
relations between restored objects point at the copies when the target
was restored first, at the original otherwise.

What the API cannot recreate is reported on the record it belonged to,
`blocks.json` for a block, and the rest of the page is restored:

- `unsupported`, `link_preview` and `template` blocks;
- a page under a block, a toggle or a column say, is created under the
  enclosing page instead, after the blocks it came with;
- `rollup`, `status`, `unique_id` and `button` database properties, and
  relations to a database that is not in the restore, the database
  itself included;
- computed values: formulas, rollups, created and edited times and users;
- a hosted file the backup did not include, or larger than 20 MB, the
  single-part upload limit;
- hosted files in a `files` property of a page or row: the backup keeps
  the property with its links, which expire, and does not download them;
- comments, which the API does not list for a backup.

## API version

The integration speaks API version `2022-06-28`. Notion's `2025-09-03`
version moved database schemas and queries behind data sources; under
the older version a database with more than one data source is read and
written through its first one, and the API refuses the operations that
would be ambiguous. Moving to data sources is a change to the client
package only.
