# Notion integration for plakar

Backs up and restores a Notion workspace through the public API
(version 2026-03-11): pages, databases and their data sources, rows,
blocks, files hosted by Notion, comments and views.

## Setting Notion up

1. Create an internal connection in the
   [developer portal](https://www.notion.so/profile/integrations) and
   copy its token (`ntn_...`).
2. Give it the capabilities it needs: *read content* for backups,
   *insert content* and *update content* for restores, *read comments*
   and *insert comments* for the comments, *user information* for the
   member list and people properties.
3. Share the pages and databases to back up with the connection
   (page menu, *Connections*, or the portal's *Content access* tab).
   Children inherit the access.

The API does not let an internal connection create pages at the top of
a workspace, so a restore needs a page to restore under.

## Backup

```
$ plakar source add mynotion notion:// token=ntn_...
$ plakar at @mystore backup @mynotion
```

`notion://` backs up everything the connection can reach,
`notion://<page or database id>` a single subtree. The token can also
come from the `NOTION_TOKEN` environment variable.

Options:

| option     | default | meaning                                                               |
|------------|---------|-----------------------------------------------------------------------|
| `comments` | `pages` | `none`, `pages` (open discussions of each page) or `blocks` (every block too, one request per block) |
| `users`    | `true`  | back up the member list                                               |
| `views`    | `true`  | back up the views of each database                                    |
| `rate`     | `3`     | requests per second; Business and Enterprise plans allow `10`         |

## Restore

```
$ plakar destination add mynotion-restore notion://<page id> token=ntn_...
$ plakar at @mystore restore -to @mynotion-restore <snapshot>
$ plakar at @mystore restore -to @mynotion-restore <snapshot>:/pages/Engineering\ <id>
```

Notion assigns new ids to everything it creates, so a restore builds a
copy of the backup under the target page: pages, databases, rows,
blocks, files, comments and views are recreated in their original
order, and the references between them (parents, relations, mentions,
links, synced blocks) point at the copies. Restoring twice gives two
copies. Restoring a subtree grafts it under the target page.

Options: `comments` and `views` (booleans, default `true`), `rate`.

## What a snapshot looks like

```
/workspace.json                   the connection's bot user and workspace
/users.json                       the members
/pages/<Title id>/                a top-level page
    about.json                    the page object, as the API returns it
    assets/<file id>/<name>       the files Notion hosts for the page
    blocks.json                   the block tree, with children nested
    comments.json                 the open comments
    content.md                    the page as Notion-flavored markdown, files linked to assets/
    databases/<Title id>/         child databases
    pages/<Title id>/             child pages
/databases/<Title id>/            a top-level database
    about.json
    assets/
    sources/<Title id>/           its data sources
        about.json                the schema
        rows/<Title id>/          its rows, laid out like pages
    views.json
```

Directories are named after the object's title and id, so a renamed
page moves in the snapshot but is never confused with another.

## How the backup keeps the API budget

The Notion API allows about three requests per second. A page costs one
request for its markdown, which tells its children and the files it
embeds, and one for its comments. The block tree, the files and the
markdown are only fetched when plakar reads them, which it does not
for a page whose `last_edited_time` has not moved since the previous
snapshot: a second backup of an unchanged workspace is two requests per
page.

## Limits

- Pages and databases are created as copies with new ids; the originals
  are never touched.
- A child database and a page that lived inside a toggle or a column
  are restored at the end of their page, the API cannot place them.
- Blocks the API cannot create are dropped with a warning
  (`unsupported`, templates, meeting notes); link previews become
  bookmarks; a synced block whose original is out of reach becomes a
  plain copy.
- Resolved comments are not exposed by the API. Restored comments are
  written by the connection, with the original author as display name.
- Views are recreated best effort: the API refuses some configurations,
  which then come back with their name and type only. Notion also adds
  its default view to every database it creates.
- Pages in the trash are not backed up.
- A data source query stops at 10,000 rows; the backup resumes it from
  the creation time of the last row, so only rows created in the same
  minute beyond that limit could be missed.
- File uploads follow the workspace plan (5 MiB per file on free
  workspaces, 5 GiB otherwise).
