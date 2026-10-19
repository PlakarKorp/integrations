package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeID(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "dashed", in: "0f6b1a2c-3d4e-4f50-8a9b-0c1d2e3f4a5b", want: "0f6b1a2c-3d4e-4f50-8a9b-0c1d2e3f4a5b"},
		{name: "bare", in: "0F6B1A2C3D4E4F508A9B0C1D2E3F4A5B", want: "0f6b1a2c-3d4e-4f50-8a9b-0c1d2e3f4a5b"},
		{name: "spaces", in: "  0f6b1a2c3d4e4f508a9b0c1d2e3f4a5b\n", want: "0f6b1a2c-3d4e-4f50-8a9b-0c1d2e3f4a5b"},
		{name: "short", in: "0f6b1a2c", wantErr: true},
		{name: "not hex", in: "zf6b1a2c3d4e4f508a9b0c1d2e3f4a5b", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeID(tc.in)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrNotAnID)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseLocation(t *testing.T) {
	const id = "0f6b1a2c-3d4e-4f50-8a9b-0c1d2e3f4a5b"
	for _, tc := range []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "workspace", in: "notion://", want: ""},
		{name: "id", in: "notion://" + id, want: id},
		{name: "bare id", in: "notion://0f6b1a2c3d4e4f508a9b0c1d2e3f4a5b", want: id},
		{name: "trailing slash", in: "notion://" + id + "/", want: id},
		{name: "pasted url", in: "notion://www.notion.so/acme/My-Page-0f6b1a2c3d4e4f508a9b0c1d2e3f4a5b?v=abc&pvs=4", want: id},
		{name: "pasted url with fragment", in: "notion://www.notion.so/My-Page-0f6b1a2c3d4e4f508a9b0c1d2e3f4a5b#deadbeef", want: id},
		{name: "garbage", in: "notion://not-an-id", wantErr: true},
		{name: "host only", in: "notion://www.notion.so", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLocation(tc.in)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrNotAnID)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestObjectEnvelope(t *testing.T) {
	raw := `{"object":"page","id":"p1","parent":{"type":"database_id","database_id":"d1"},"last_edited_time":"2026-01-02T03:04:05.000Z","properties":{"Name":{"type":"title","title":[{"type":"text","plain_text":"Hello "},{"type":"text","plain_text":"world"}]}},"extra":1}`
	var o Object
	require.NoError(t, json.Unmarshal([]byte(raw), &o))
	require.Equal(t, "p1", o.ID)
	require.Equal(t, "d1", o.Parent.ID())
	require.Equal(t, 2026, o.LastEditedTime.Year())
	require.Equal(t, "Hello world", o.Title())
	m, err := o.Map()
	require.NoError(t, err)
	require.EqualValues(t, 1, m["extra"])

	var list List
	require.NoError(t, json.Unmarshal([]byte(`{"results":[`+raw+`],"has_more":true,"next_cursor":"c"}`), &list))
	require.Len(t, list.Results, 1)
	require.Equal(t, "p1", list.Results[0].ID)
	require.JSONEq(t, raw, string(list.Results[0].Raw))
}

func TestTitleOf(t *testing.T) {
	require.Equal(t, "DB", TitleOf(map[string]any{"object": "database", "title": []any{map[string]any{"plain_text": "DB"}}}))
	require.Equal(t, "Child", TitleOf(map[string]any{"object": "block", "type": "child_page", "child_page": map[string]any{"title": "Child"}}))
	require.Equal(t, "", TitleOf(map[string]any{"object": "block", "type": "paragraph"}))
	require.Equal(t, "", TitleOf(map[string]any{"object": "page", "properties": map[string]any{"x": map[string]any{"type": "number"}}}))
	require.Equal(t, "", Parent{Type: "workspace", Workspace: true}.ID())
	require.Equal(t, "b", Parent{Type: "block_id", BlockID: "b"}.ID())
}
