package testhelpers

import (
	"context"
	"io"
	"strings"
	"testing"

	tcexec "github.com/testcontainers/testcontainers-go/exec"

	"github.com/testcontainers/testcontainers-go"
)

// ExecOK runs cmd inside container, streams the combined output to t.Log, and
// fails the test if the exit code is non-zero.
func ExecOK(ctx context.Context, t *testing.T, container testcontainers.Container, cmd ...string) {
	t.Helper()
	t.Logf("$ %s", strings.Join(cmd, " "))
	code, out, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec %v: %v", cmd, err)
	}
	b, _ := io.ReadAll(out)
	if s := strings.TrimSpace(string(b)); s != "" {
		t.Log(s)
	}
	if code != 0 {
		t.Fatalf("exec %v: exited %d", cmd, code)
	}
}

// ExecCapture runs cmd inside container and returns its combined output as a
// string. The test fails if the exit code is non-zero.
func ExecCapture(ctx context.Context, t *testing.T, container testcontainers.Container, cmd ...string) string {
	t.Helper()
	t.Logf("$ %s", strings.Join(cmd, " "))
	code, out, err := container.Exec(ctx, cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec %v: %v", cmd, err)
	}
	b, _ := io.ReadAll(out)
	s := strings.TrimSpace(string(b))
	if code != 0 {
		t.Fatalf("exec %v: exited %d\n%s", cmd, code, s)
	}
	return s
}
