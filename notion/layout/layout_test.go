/*
 * Copyright (c) 2026 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package layout

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	idA = "11111111-2222-3333-4444-555555555555"
	idB = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	idC = "99999999-8888-7777-6666-555555555555"
)

func TestDirName(t *testing.T) {
	for _, tc := range []struct {
		title, want string
	}{
		{"Engineering Wiki", "Engineering Wiki 11111111222233334444555555555555"},
		{"", "Untitled 11111111222233334444555555555555"},
		{"   ", "Untitled 11111111222233334444555555555555"},
		{"a/b\\c:d", "a-b-c-d 11111111222233334444555555555555"},
		{".hidden", "_hidden 11111111222233334444555555555555"},
		{"tab\there", "tab here 11111111222233334444555555555555"},
		{strings.Repeat("x", 200), strings.Repeat("x", 120) + " 11111111222233334444555555555555"},
		{strings.Repeat("é", 100), strings.Repeat("é", 60) + " 11111111222233334444555555555555"},
	} {
		got := DirName(tc.title, idA)
		require.Equal(t, tc.want, got, tc.title)
		id, ok := ParseDirName(got)
		require.True(t, ok)
		require.Equal(t, idA, id)
	}

	_, ok := ParseDirName("no id here")
	require.False(t, ok)
	_, ok = ParseDirName("pages")
	require.False(t, ok)
}

func TestClassify(t *testing.T) {
	page := "/pages/" + DirName("Root", idA)
	db := page + "/databases/" + DirName("Tasks", idB)
	ds := db + "/sources/" + DirName("Tasks", idC)
	row := ds + "/rows/" + DirName("Task 1", idA)

	for _, tc := range []struct {
		path string
		want Path
	}{
		{"/", Path{Kind: KindRoot}},
		{"/workspace.json", Path{Kind: KindRootFile}},
		{"/users.json", Path{Kind: KindRootFile}},
		{"/pages", Path{Kind: KindContainer}},
		{"/databases", Path{Kind: KindContainer}},
		{"/sources", Path{Kind: KindUnknown}},
		{"/about.json", Path{Kind: KindUnknown}},
		{page, Path{Kind: KindEntity, Entity: page, Object: EntityPage, ID: idA}},
		{page + "/about.json", Path{Kind: KindAbout, Entity: page, Object: EntityPage, ID: idA}},
		{page + "/assets", Path{Kind: KindAssetsDir, Entity: page, Object: EntityPage, ID: idA}},
		{page + "/assets/f1/photo.png", Path{Kind: KindAsset, Entity: page, Object: EntityPage, ID: idA, AssetKey: "f1/photo.png"}},
		{page + "/assets/f1", Path{Kind: KindAssetsDir, Entity: page, Object: EntityPage, ID: idA}},
		{page + "/assets/f1/x/y", Path{Kind: KindUnknown}},
		{page + "/blocks.json", Path{Kind: KindBlocks, Entity: page, Object: EntityPage, ID: idA}},
		{page + "/comments.json", Path{Kind: KindComments, Entity: page, Object: EntityPage, ID: idA}},
		{page + "/content.md", Path{Kind: KindContent, Entity: page, Object: EntityPage, ID: idA}},
		{page + "/views.json", Path{Kind: KindUnknown}},
		{page + "/pages", Path{Kind: KindContainer, Entity: page, Object: EntityPage, ID: idA}},
		{page + "/rows", Path{Kind: KindUnknown}},
		{db, Path{Kind: KindEntity, Entity: db, Object: EntityDatabase, ID: idB, ParentDir: page}},
		{db + "/about.json", Path{Kind: KindAbout, Entity: db, Object: EntityDatabase, ID: idB, ParentDir: page}},
		{db + "/views.json", Path{Kind: KindViews, Entity: db, Object: EntityDatabase, ID: idB, ParentDir: page}},
		{db + "/blocks.json", Path{Kind: KindUnknown}},
		{ds, Path{Kind: KindEntity, Entity: ds, Object: EntityDataSource, ID: idC, ParentDir: db}},
		{ds + "/about.json", Path{Kind: KindAbout, Entity: ds, Object: EntityDataSource, ID: idC, ParentDir: db}},
		{ds + "/assets/x/y", Path{Kind: KindUnknown}},
		{row, Path{Kind: KindEntity, Entity: row, Object: EntityPage, ID: idA, ParentDir: ds}},
		{row + "/blocks.json", Path{Kind: KindBlocks, Entity: row, Object: EntityPage, ID: idA, ParentDir: ds}},
		{"/pages/not an id/about.json", Path{Kind: KindUnknown}},
	} {
		got := Classify(tc.path)
		require.Equal(t, tc.want, got, tc.path)
	}
}

// TestOrder pins the property the restore relies on: within an
// object's directory, a lexical walk yields the object, its assets,
// its blocks, its comments, then its children.
func TestOrder(t *testing.T) {
	names := []string{PagesDir, DatabasesDir, CommentsFile, BlocksFile, AssetsDir, AboutFile, ContentFile, SourcesDir, ViewsFile, RowsDir}
	sort.Strings(names)
	require.Equal(t, []string{AboutFile, AssetsDir, BlocksFile, CommentsFile, ContentFile, DatabasesDir, PagesDir, RowsDir, SourcesDir, ViewsFile}, names)
}

func TestContainerFor(t *testing.T) {
	require.Equal(t, "/pages", ContainerFor("", EntityNone, EntityPage))
	require.Equal(t, "/databases", ContainerFor("", EntityNone, EntityDatabase))
	require.Equal(t, "/pages/P 1/pages", ContainerFor("/pages/P 1", EntityPage, EntityPage))
	require.Equal(t, "/pages/P 1/databases", ContainerFor("/pages/P 1", EntityPage, EntityDatabase))
	require.Equal(t, "/databases/D 1/sources", ContainerFor("/databases/D 1", EntityDatabase, EntityDataSource))
	require.Equal(t, "/databases/D 1/pages", ContainerFor("/databases/D 1", EntityDatabase, EntityPage))
	require.Equal(t, "/databases/D 1/sources/S 1/rows", ContainerFor("/databases/D 1/sources/S 1", EntityDataSource, EntityPage))
}

func TestScanAndRewrite(t *testing.T) {
	md := `# Title

Some text with an image:

![a photo](https://prod-files-secure.s3.us-west-2.amazonaws.com/ws-1/11111111-2222-3333-4444-555555555555/photo%20one.png?X-Amz-Signature=abc&amp;X-Amz-Expires=3600)

<file source="https://file.notion.so/f/f/ws/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/report.pdf?table=block&id=x&expirationTimestamp=1">report</file>

<page url="https://www.notion.so/Child-Page-99999999888877776666555555555555">Child Page</page>
<page color="red" url="https://www.notion.so/11111111222233334444555555555555">Other</page>

<database url="https://www.notion.so/aaaaaaaabbbbccccddddeeeeeeeeeeee" data-source-url="https://www.notion.so/99999999888877776666555555555555" inline="true">Tasks</database>

[external](https://example.com/not-notion.png)
[typed](https://prod-files-secure.s3.us-west-2.amazonaws.com/ws-1/99999999888877776666555555555555/typed.png?x=1)
`
	pages, dbs, files := Scan(md)
	require.Equal(t, []string{idC, idA}, pages)
	require.Equal(t, []DatabaseLink{{ID: idB, DataSourceID: idC}}, dbs)
	require.Len(t, files, 2, "a plain link to a Notion file host is not a file of the page")
	require.Contains(t, files[0], "photo%20one.png?X-Amz-Signature=abc&X-Amz-Expires=3600")
	require.Contains(t, files[1], "report.pdf")

	out := Rewrite(md, func(u string) (string, bool) {
		if strings.Contains(u, "photo") {
			return "assets/11111111222233334444555555555555/photo one.png", true
		}
		return "", false
	})
	require.Contains(t, out, "![a photo](assets/11111111222233334444555555555555/photo%20one.png)")
	require.Contains(t, out, "file.notion.so/f/f/ws/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/report.pdf")
	require.Contains(t, out, "https://example.com/not-notion.png")
}
