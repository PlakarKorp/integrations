package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

func TestBlockForCreate(t *testing.T) {
	ids := func(old string) (string, bool) {
		if old == "old-page" {
			return "new-page", true
		}
		return "", false
	}

	t.Run("paragraph loses derived fields", func(t *testing.T) {
		block := decodeJSON(t, `{"object":"block","id":"b1","parent":{"type":"page_id","page_id":"p"},"created_time":"t","last_edited_time":"t","created_by":{"object":"user","id":"u"},"has_children":false,"archived":false,"in_trash":false,"type":"paragraph","paragraph":{"rich_text":[{"type":"text","text":{"content":"hi","link":null},"annotations":{"bold":true},"plain_text":"hi","href":null},{"type":"mention","mention":{"type":"user","user":{"object":"user","id":"u1","name":"Ann","avatar_url":"x","type":"person","person":{}}},"annotations":{},"plain_text":"@Ann","href":null},{"type":"mention","mention":{"type":"page","page":{"id":"old-page"}},"annotations":{},"plain_text":"p","href":"u"}],"color":"default"}}`)
		body, ok := BlockForCreate(block, ids)
		require.True(t, ok)
		require.Equal(t, map[string]any{"object": "block", "type": "paragraph", "paragraph": map[string]any{
			"rich_text": []any{
				map[string]any{"type": "text", "text": map[string]any{"content": "hi", "link": nil}, "annotations": map[string]any{"bold": true}},
				map[string]any{"type": "mention", "mention": map[string]any{"type": "user", "user": map[string]any{"object": "user", "id": "u1"}}, "annotations": map[string]any{}},
				map[string]any{"type": "mention", "mention": map[string]any{"type": "page", "page": map[string]any{"id": "new-page"}}, "annotations": map[string]any{}},
			},
			"color": "default",
		}}, body)
		// The input is left alone.
		require.Contains(t, block["paragraph"].(map[string]any)["rich_text"].([]any)[0], "plain_text")
	})

	t.Run("not creatable", func(t *testing.T) {
		for _, typ := range []string{"child_page", "child_database", "unsupported", "link_preview", "template", ""} {
			_, ok := BlockForCreate(map[string]any{"type": typ, typ: map[string]any{}}, nil)
			require.False(t, ok, typ)
		}
	})

	t.Run("hosted callout icon dropped, link_to_page remapped", func(t *testing.T) {
		body, ok := BlockForCreate(decodeJSON(t, `{"type":"callout","callout":{"rich_text":[],"icon":{"type":"file","file":{"url":"https://x","expiry_time":"t"}},"color":"gray_background"}}`), nil)
		require.True(t, ok)
		require.NotContains(t, body["callout"], "icon")

		body, ok = BlockForCreate(decodeJSON(t, `{"type":"callout","callout":{"rich_text":[],"icon":{"type":"emoji","emoji":"💡"}}}`), nil)
		require.True(t, ok)
		require.Equal(t, "💡", body["callout"].(map[string]any)["icon"].(map[string]any)["emoji"])

		body, ok = BlockForCreate(decodeJSON(t, `{"type":"link_to_page","link_to_page":{"type":"page_id","page_id":"old-page"}}`), ids)
		require.True(t, ok)
		require.Equal(t, "new-page", body["link_to_page"].(map[string]any)["page_id"])

		body, ok = BlockForCreate(decodeJSON(t, `{"type":"link_to_page","link_to_page":{"type":"page_id","page_id":"elsewhere"}}`), ids)
		require.True(t, ok)
		require.Equal(t, "elsewhere", body["link_to_page"].(map[string]any)["page_id"])
	})

	t.Run("file payload kept for the caller to upload", func(t *testing.T) {
		block := decodeJSON(t, `{"type":"image","image":{"type":"file","file":{"url":"https://host/x.png","expiry_time":"t"},"caption":[]}}`)
		require.Equal(t, "https://host/x.png", HostedFileURL(block["image"]))
		require.Equal(t, "", HostedFileURL(map[string]any{"type": "external"}))
		body, ok := BlockForCreate(block, nil)
		require.True(t, ok)
		payload := body["image"].(map[string]any)
		require.True(t, IsHostedFile(payload))
		SetFileUpload(payload, "up-1")
		require.Equal(t, map[string]any{"type": "file_upload", "file_upload": map[string]any{"id": "up-1"}, "caption": []any{}}, payload)
	})

	require.True(t, NeedsInlineChildren("column_list"))
	require.True(t, NeedsInlineChildren("table"))
	require.False(t, NeedsInlineChildren("toggle"))
	require.True(t, IsFileBlock("pdf"))
	require.False(t, IsFileBlock("bookmark"))
}

const rowJSON = `{"object":"page","id":"r1","parent":{"type":"database_id","database_id":"db-old"},"icon":{"type":"emoji","emoji":"🚀"},"cover":{"type":"file","file":{"url":"https://x","expiry_time":"t"}},"properties":{
 "Name":{"id":"title","type":"title","title":[{"type":"text","text":{"content":"Row"},"annotations":{},"plain_text":"Row","href":null}]},
 "Done":{"id":"a","type":"checkbox","checkbox":true},
 "Tag":{"id":"b","type":"select","select":{"id":"opt1","name":"urgent","color":"red"}},
 "Tags":{"id":"c","type":"multi_select","multi_select":[{"id":"o1","name":"x","color":"blue"},{"id":"o2","name":"y","color":"green"}]},
 "State":{"id":"d","type":"status","status":{"id":"s","name":"Doing","color":"blue"}},
 "Owner":{"id":"e","type":"people","people":[{"object":"user","id":"u1","name":"Ann","type":"person"}]},
 "Link":{"id":"f","type":"relation","relation":[{"id":"old-row"},{"id":"unknown-row"}],"has_more":false},
 "Files":{"id":"g","type":"files","files":[{"name":"ext","type":"external","external":{"url":"https://e"}},{"name":"hosted","type":"file","file":{"url":"https://h","expiry_time":"t"}}]},
 "Sum":{"id":"h","type":"formula","formula":{"type":"number","number":3}},
 "Roll":{"id":"i","type":"rollup","rollup":{"type":"number","number":1}},
 "Made":{"id":"j","type":"created_time","created_time":"t"},
 "ID":{"id":"k","type":"unique_id","unique_id":{"prefix":"T","number":1}},
 "When":{"id":"l","type":"date","date":{"start":"2026-01-01","end":null,"time_zone":null}},
 "Empty":{"id":"m","type":"date","date":null},
 "Num":{"id":"n","type":"number","number":4.5}
}}`

func TestPageForCreate(t *testing.T) {
	ids := func(old string) (string, bool) {
		if old == "old-row" {
			return "new-row", true
		}
		return "", false
	}
	parent := map[string]any{"type": "database_id", "database_id": "db-new"}

	body, hostedIcon, hostedCover := PageForCreate(decodeJSON(t, rowJSON), PageOptions{Parent: parent, Drop: map[string]bool{"State": true}, IDs: ids})
	require.False(t, hostedIcon)
	require.True(t, hostedCover)
	require.Equal(t, parent, body["parent"])
	require.Equal(t, map[string]any{"type": "emoji", "emoji": "🚀"}, body["icon"])
	require.NotContains(t, body, "cover")

	props := body["properties"].(map[string]any)
	require.Equal(t, map[string]any{
		"Name":  map[string]any{"title": []any{map[string]any{"type": "text", "text": map[string]any{"content": "Row"}, "annotations": map[string]any{}}}},
		"Done":  map[string]any{"checkbox": true},
		"Tag":   map[string]any{"select": map[string]any{"name": "urgent"}},
		"Tags":  map[string]any{"multi_select": []any{map[string]any{"name": "x"}, map[string]any{"name": "y"}}},
		"Owner": map[string]any{"people": []any{map[string]any{"object": "user", "id": "u1"}}},
		"Link":  map[string]any{"relation": []any{map[string]any{"id": "new-row"}}},
		"Files": map[string]any{"files": []any{map[string]any{"name": "ext", "type": "external", "external": map[string]any{"url": "https://e"}}}},
		"When":  map[string]any{"date": map[string]any{"start": "2026-01-01", "end": nil, "time_zone": nil}},
		"Num":   map[string]any{"number": 4.5},
	}, props)

	t.Run("title only", func(t *testing.T) {
		parent := map[string]any{"type": "page_id", "page_id": "p"}
		body, _, _ := PageForCreate(decodeJSON(t, rowJSON), PageOptions{Parent: parent, TitleOnly: true})
		require.Equal(t, map[string]any{
			"title": map[string]any{"title": []any{map[string]any{"type": "text", "text": map[string]any{"content": "Row"}, "annotations": map[string]any{}}}},
		}, body["properties"])
	})

	t.Run("no id map keeps relations", func(t *testing.T) {
		body, _, _ := PageForCreate(decodeJSON(t, rowJSON), PageOptions{Parent: parent})
		rel := body["properties"].(map[string]any)["Link"].(map[string]any)["relation"]
		require.Equal(t, []any{map[string]any{"id": "old-row"}, map[string]any{"id": "unknown-row"}}, rel)
	})

	t.Run("no properties", func(t *testing.T) {
		body, _, _ := PageForCreate(map[string]any{}, PageOptions{Parent: parent})
		require.Equal(t, map[string]any{}, body["properties"])
	})
}

const dbJSON = `{"object":"database","id":"db-old","parent":{"type":"page_id","page_id":"p"},"title":[{"type":"text","text":{"content":"Tasks"},"annotations":{},"plain_text":"Tasks","href":null}],"description":[],"is_inline":true,"icon":{"type":"file","file":{"url":"https://x"}},"cover":{"type":"external","external":{"url":"https://c"}},"url":"u","properties":{
 "Name":{"id":"title","name":"Name","type":"title","title":{}},
 "Tag":{"id":"a","name":"Tag","type":"select","select":{"options":[{"id":"o1","name":"urgent","color":"red","description":null}]}},
 "Tags":{"id":"b","name":"Tags","type":"multi_select","multi_select":{"options":[{"id":"o2","name":"x","color":"blue"}]}},
 "State":{"id":"c","name":"State","type":"status","status":{"options":[],"groups":[]}},
 "Self":{"id":"d","name":"Self","type":"relation","relation":{"database_id":"db-old","type":"single_property","single_property":{}}},
 "Other":{"id":"e","name":"Other","type":"relation","relation":{"database_id":"db-other","type":"dual_property","dual_property":{"synced_property_name":"Back","synced_property_id":"zz"}}},
 "Gone":{"id":"f","name":"Gone","type":"relation","relation":{"database_id":"db-missing","type":"single_property","single_property":{}}},
 "Roll":{"id":"g","name":"Roll","type":"rollup","rollup":{}},
 "Num":{"id":"h","name":"Num","type":"number","number":{"format":"euro"}},
 "Sum":{"id":"i","name":"Sum","type":"formula","formula":{"expression":"prop(\"Num\")*2"}},
 "Made":{"id":"j","name":"Made","type":"created_time","created_time":{}},
 "ID":{"id":"k","name":"ID","type":"unique_id","unique_id":{"prefix":"T"}}
}}`

func TestDatabaseForCreate(t *testing.T) {
	ids := func(old string) (string, bool) {
		if old == "db-other" {
			return "db-other-new", true
		}
		return "", false
	}
	parent := map[string]any{"type": "page_id", "page_id": "target"}
	body, dropped, hostedIcon, hostedCover := DatabaseForCreate(decodeJSON(t, dbJSON), parent, ids)

	require.True(t, hostedIcon)
	require.False(t, hostedCover)
	require.Equal(t, parent, body["parent"])
	require.Equal(t, true, body["is_inline"])
	require.Equal(t, map[string]any{"type": "external", "external": map[string]any{"url": "https://c"}}, body["cover"])
	require.NotContains(t, body, "icon")
	require.NotContains(t, body, "url")
	require.Equal(t, []any{map[string]any{"type": "text", "text": map[string]any{"content": "Tasks"}, "annotations": map[string]any{}}}, body["title"])

	require.Equal(t, map[string]bool{"State": true, "Self": true, "Gone": true, "Roll": true, "ID": true}, dropped)
	require.Equal(t, map[string]any{
		"Name":  map[string]any{"title": map[string]any{}},
		"Tag":   map[string]any{"select": map[string]any{"options": []any{map[string]any{"name": "urgent", "color": "red"}}}},
		"Tags":  map[string]any{"multi_select": map[string]any{"options": []any{map[string]any{"name": "x", "color": "blue"}}}},
		"Other": map[string]any{"relation": map[string]any{"database_id": "db-other-new", "type": "dual_property", "dual_property": map[string]any{}}},
		"Num":   map[string]any{"number": map[string]any{"format": "euro"}},
		"Sum":   map[string]any{"formula": map[string]any{"expression": `prop("Num")*2`}},
		"Made":  map[string]any{"created_time": map[string]any{}},
	}, body["properties"])

	t.Run("no id map drops every relation", func(t *testing.T) {
		_, dropped, _, _ := DatabaseForCreate(decodeJSON(t, dbJSON), parent, nil)
		require.True(t, dropped["Other"])
		require.True(t, dropped["Self"])
	})
}
