// Package e2e drives a real kloset backup and restore through the Notion
// importer and exporter, against the fake API, so the connectors are
// exercised the way plakar runs them rather than the way the unit tests
// call them.
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"path"
	"testing"

	"github.com/PlakarKorp/integrations/notion/api"
	"github.com/PlakarKorp/integrations/notion/exporter"
	"github.com/PlakarKorp/integrations/notion/importer"
	"github.com/PlakarKorp/integrations/notion/internal/fakenotion"
	"github.com/PlakarKorp/integrations/notion/layout"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/snapshot"
	ptesting "github.com/PlakarKorp/kloset/testing"
	"github.com/stretchr/testify/require"
)

func TestBackupAndRestoreThroughKloset(t *testing.T) {
	srv := fakenotion.New(t)
	root := srv.AddPage("workspace", "", "Root", nil)
	srv.AddBlock(root, "paragraph", map[string]any{"rich_text": fakenotion.RichText("hello"), "color": "default"})
	picture := []byte("\x89PNG not really")
	img := srv.AddFile("pic.png", picture)
	img["caption"] = []any{}
	image := srv.AddBlock(root, "image", img)
	child := srv.AddPage("page_id", root, "Child", nil)
	srv.AddBlock(child, "paragraph", map[string]any{"rich_text": fakenotion.RichText("child text"), "color": "default"})
	db := srv.AddDatabase(root, "Tasks", nil)
	srv.AddPage("database_id", db, "a row", nil)

	repo := ptesting.GenerateRepository(t, nil, nil, nil)
	ctx := repo.AppContext()
	config := func(location string) map[string]string {
		return map[string]string{"location": location, "token": fakenotion.Token, "api_url": srv.URL()}
	}

	// Backup.
	imp, err := importer.NewImporter(ctx, &connectors.Options{MaxConcurrency: 4}, "notion", config("notion://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = imp.Close(context.Background()) })

	builder, err := snapshot.Create(repo, repository.DefaultType, "", objects.NilMac, &snapshot.BuilderOptions{})
	require.NoError(t, err)
	source, err := snapshot.NewSource(ctx, imp)
	require.NoError(t, err)
	require.NoError(t, builder.Backup(source))
	require.NoError(t, builder.Commit())
	require.NoError(t, builder.Close())
	require.NoError(t, builder.Repository().RebuildState())

	snap, err := snapshot.Load(repo, builder.Header.Identifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = snap.Close() })
	require.Zero(t, snap.Header.GetSource(0).Summary.Directory.Errors+snap.Header.GetSource(0).Summary.Below.Errors)

	vfs, err := snap.Filesystem()
	require.NoError(t, err)
	read := func(p string) []byte {
		f, err := vfs.Open(p)
		require.NoError(t, err, p)
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(f)
		require.NoError(t, err, p)
		return data
	}
	rootDir := layout.PageDir("", root)
	var page map[string]any
	require.NoError(t, json.Unmarshal(read(path.Join(rootDir, layout.PageFile)), &page))
	require.Equal(t, srv.Page(root), page)
	require.Equal(t, picture, read(path.Join(rootDir, layout.FilesDir, image+".png")))
	entry, err := vfs.GetEntry(path.Join(rootDir, layout.FilesDir, image+".png"))
	require.NoError(t, err)
	require.Equal(t, int64(len(picture)), entry.Stat().Size(), "a lazily read file must get its real size")
	require.NotEmpty(t, read(path.Join(layout.DatabaseDir(rootDir, db), layout.DatabaseFile)))

	// Restore the whole snapshot under a page.
	target := srv.AddPage("workspace", "", "Target", nil)
	exp, err := exporter.NewExporter(ctx, &connectors.Options{MaxConcurrency: 4}, "notion", config("notion://"+target))
	require.NoError(t, err)
	require.NoError(t, snap.Export(exp, "/", &snapshot.ExportOptions{}))
	require.NoError(t, exp.Close(context.Background()))

	under := srv.Children(target)
	require.Len(t, under, 1)
	newRoot := under[0]["id"].(string)
	require.Equal(t, "Root", api.TitleOf(srv.Page(newRoot)))
	blocks := srv.Children(newRoot)
	require.Len(t, blocks, 4)
	require.Equal(t, "paragraph", blocks[0]["type"])
	require.Equal(t, "image", blocks[1]["type"])
	upload := blocks[1]["image"].(map[string]any)["file_upload"].(map[string]any)["id"].(string)
	require.Equal(t, picture, srv.Upload(upload))
	require.Equal(t, "child_page", blocks[2]["type"])
	require.Equal(t, "child_database", blocks[3]["type"])
	require.Len(t, srv.Rows(blocks[3]["id"].(string)), 1)

	// Restore one page of it: plakar strips the prefix, the exporter must
	// cope with the page at the top.
	target2 := srv.AddPage("workspace", "", "Target 2", nil)
	exp2, err := exporter.NewExporter(ctx, &connectors.Options{MaxConcurrency: 4}, "notion", config("notion://"+target2))
	require.NoError(t, err)
	require.NoError(t, snap.Export(exp2, layout.PageDir(rootDir, child), &snapshot.ExportOptions{}))
	require.NoError(t, exp2.Close(context.Background()))

	under = srv.Children(target2)
	require.Len(t, under, 1)
	require.Equal(t, "Child", api.TitleOf(srv.Page(under[0]["id"].(string))))
	kids := srv.Children(under[0]["id"].(string))
	require.Len(t, kids, 1)
	require.Equal(t, "paragraph", kids[0]["type"])
}
