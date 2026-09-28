package k8s

import (
	"path"
	"strings"
	"testing"

	"github.com/PlakarKorp/kloset/exclude"
	"github.com/stretchr/testify/require"
)

func TestRebaseExcludes(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		excludes []string
		want     []string
	}{
		{
			name:     "no excludes",
			prefix:   "/",
			excludes: nil,
			want:     nil,
		},
		{
			name:     "unanchored patterns are kept as-is",
			prefix:   "/",
			excludes: []string{"*.log", "cache/", "!keep.log"},
			want:     []string{"*.log", "cache/", "!keep.log"},
		},
		{
			name:     "double star prefixed patterns are kept as-is",
			prefix:   "/vol/data",
			excludes: []string{"**/node_modules/", "**/var/log"},
			want:     []string{"**/node_modules/", "**/var/log"},
		},
		{
			name:     "anchored patterns are rerooted",
			prefix:   "/",
			excludes: []string{"/var/log", "var/tmp/", "!/var/log/keepme"},
			want:     []string{"/data/var/log", "/data/var/tmp/", "!/data/var/log/keepme"},
		},
		{
			name:     "the prefix is stripped",
			prefix:   "/rootdisk/data",
			excludes: []string{"/rootdisk/data/var/log"},
			want:     []string{"/data/var/log"},
		},
		{
			name:     "globs in the prefix are honored",
			prefix:   "/rootdisk/data",
			excludes: []string{"/*/data/var/log"},
			want:     []string{"/data/var/log"},
		},
		{
			name:     "double star swallows the prefix",
			prefix:   "/rootdisk/data",
			excludes: []string{"/**/log"},
			want:     []string{"/data/**/log"},
		},
		{
			name:     "patterns for another disk are dropped",
			prefix:   "/rootdisk/data",
			excludes: []string{"/otherdisk/data/var/log", "/rootdisk/config.yaml"},
			want:     nil,
		},
		{
			name:     "a parent of the mount point excludes everything",
			prefix:   "/rootdisk/data",
			excludes: []string{"/rootdisk/"},
			want:     []string{"/data/"},
		},
		{
			name:     "blank lines and comments are dropped",
			prefix:   "/",
			excludes: []string{"", "# /var/log", "/var/log"},
			want:     []string{"/data/var/log"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := rebaseExcludes(test.excludes, test.prefix, fsPath)
			require.Equal(t, test.want, got)
		})
	}
}

// TestRebaseExcludesMatchesSnapshotPaths checks that the rebased rules
// exclude, on the paths the pod-side importer sees, exactly what the
// original rules exclude on the paths that end up in the snapshot.
func TestRebaseExcludesMatchesSnapshotPaths(t *testing.T) {
	excludes := []string{
		"*.tmp",
		"cache/",
		"/var/log",
		"!/var/log/keepme",
		"/vol/data/srv/junk",
		"/otherdisk/data/srv",
		"**/.git/",
	}

	paths := []struct {
		path  string
		isDir bool
	}{
		{"/srv/app/file.txt", false},
		{"/srv/app/file.tmp", false},
		{"/srv/cache", true},
		{"/srv/junk", true},
		{"/var/log", true},
		{"/var/log/messages", false},
		{"/var/log/keepme", false},
		{"/var/tmp/x", false},
		{"/srv/app/.git", true},
	}

	for _, prefix := range []string{"/", "/vol/data"} {
		t.Run(prefix, func(t *testing.T) {
			orig := exclude.NewRuleSet()
			require.NoError(t, orig.AddRulesFromArray(excludes))

			pod := exclude.NewRuleSet()
			require.NoError(t, pod.AddRulesFromArray(rebaseExcludes(excludes, prefix, fsPath)))

			for _, p := range paths {
				snapPath := path.Join(prefix, p.path)
				podPath := path.Join(fsPath, p.path)

				require.Equal(t, orig.IsExcluded(snapPath, p.isDir),
					pod.IsExcluded(podPath, p.isDir),
					"%s (snapshot %s, pod %s)", p.path, snapPath, podPath)
			}
		})
	}
}

func TestRebaseExcludeKeepsEscapedTrailingSpace(t *testing.T) {
	got, ok := rebaseExclude(`/var/log/foo\ `, "/", fsPath)
	require.True(t, ok)
	require.True(t, strings.HasSuffix(got, `\ `), "got %q", got)
	require.True(t, strings.HasPrefix(got, fsPath+"/"), "got %q", got)
}
