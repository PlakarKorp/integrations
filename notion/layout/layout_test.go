package layout

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirs(t *testing.T) {
	require.Equal(t, "/pages/p1", PageDir("", "p1"))
	require.Equal(t, "/pages/p1/pages/p2", PageDir("/pages/p1", "p2"))
	require.Equal(t, "/databases/d1", DatabaseDir("", "d1"))
	require.Equal(t, "/pages/p1/databases/d1", DatabaseDir("/pages/p1", "d1"))
	require.Equal(t, "/databases/d1/pages/r1", PageDir("/databases/d1", "r1"))
}

func TestFileName(t *testing.T) {
	for _, tc := range []struct{ name, url, want string }{
		{name: "plain", url: "https://host/a/b/pic.PNG", want: "b1.png"},
		{name: "signed", url: "https://s3.host/x/doc.pdf?X-Amz-Signature=abc&X-Amz-Expires=3600", want: "b1.pdf"},
		{name: "fragment", url: "https://host/movie.mp4#t=10", want: "b1.mp4"},
		{name: "none", url: "https://host/blob", want: "b1"},
		{name: "dot only", url: "https://host/file.", want: "b1"},
		{name: "odd characters", url: "https://host/file.a%20b", want: "b1"},
		{name: "too long", url: "https://host/file.abcdefghijklmnopqrstuvwxyz", want: "b1"},
		{name: "not a url", url: "", want: "b1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, FileName("b1", tc.url))
		})
	}
	require.Equal(t, "b1", StripExtension("b1.png"))
	require.Equal(t, "icon", StripExtension("icon"))
}
