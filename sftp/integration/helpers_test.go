package integration

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/PlakarKorp/integration-sdk"
	"github.com/stretchr/testify/require"
)

// concurrency is above one so that several sessions share one ssh master.
const concurrency = 4

var testDirs atomic.Int64

// testDir returns a directory under the ssh user's home that no other test,
// or other run of the same test under -count, uses.  It does not create it.
func testDir(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	return fmt.Sprintf("%s/%s-%d", home, name, testDirs.Add(1))
}

func (s *sshServer) config(root string) map[string]string {
	cfg := s.params()
	cfg["location"] = fmt.Sprintf("sftp://%s:%s%s", s.host, s.port, root)
	return cfg
}

func options() *sdk.Options {
	return &sdk.Options{MaxConcurrency: concurrency}
}

// sh runs script with sh -c as the ssh user, args bound to $1, $2...  It
// seeds and checks the server without going through the code under test.
func (s *sshServer) sh(t *testing.T, script string, args ...string) string {
	t.Helper()
	out, err := run(context.Background(), s.ctr, append([]string{"sh", "-c", script, "sh"}, args...)...)
	require.NoError(t, err)
	return out
}

func (s *sshServer) uid(t *testing.T) uint64 {
	t.Helper()
	uid, err := strconv.ParseUint(strings.TrimSpace(s.sh(t, "id -u")), 10, 32)
	require.NoError(t, err)
	return uid
}

type remoteStat struct {
	mode string // octal, special bits included
	uid  string
	size string
	ino  string
}

func (s *sshServer) stat(t *testing.T, p string) remoteStat {
	t.Helper()
	f := strings.Fields(s.sh(t, `stat -c '%a %u %s %i' "$1"`, p))
	require.Len(t, f, 4)
	return remoteStat{mode: f[0], uid: f[1], size: f[2], ino: f[3]}
}
