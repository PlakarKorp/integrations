//go:build integration

/*
 * Copyright (c) 2026 Antoine Dheygers <antoine.dheygers@cryptoweb.fr>
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

// Package e2e exercises the full backup/restore roundtrip against a real
// Incus daemon: a real Alpine container is created, backed up through the
// plugin importer, restored through the plugin exporter under another
// name, booted, and its rootfs compared against the source.
//
// Run with:
//
//	go test -tags integration -count=1 -timeout 20m ./e2e
//
// The test skips when no Incus socket is present (same convention as
// TestIncusSourcePing). It needs permission to talk to the socket
// (typically membership in the incus-admin group) and network access on
// the Incus host to download the Alpine image the first time.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"

	pexporter "github.com/PlakarKorp/integrations/incus/exporter"
	pimporter "github.com/PlakarKorp/integrations/incus/importer"
)

const socketPath = "/var/lib/incus/unix.socket"

// testImage returns the image alias and simplestreams server used to
// create the source container, overridable for hosts with a local
// mirror or a pre-seeded image.
func testImage() (alias, server string) {
	alias = os.Getenv("PLAKAR_E2E_IMAGE")
	if alias == "" {
		alias = "alpine/3.21"
	}
	server = os.Getenv("PLAKAR_E2E_IMAGE_SERVER")
	if server == "" {
		server = "https://images.linuxcontainers.org"
	}
	return alias, server
}

func TestE2EBackupRestoreRoundtrip(t *testing.T) {
	if _, err := os.Stat(socketPath); err != nil {
		t.Skip("no incus socket on this machine")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Socket present but daemon stopped or socket not readable (not in
	// incus-admin): treat like "no Incus here" rather than a failure, so
	// the suite stays green on dev machines with a dormant install.
	server, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		t.Skipf("incus socket present but unusable: %v", err)
	}
	srv, _, err := server.GetServer()
	if err != nil {
		t.Skipf("incus daemon unreachable: %v", err)
	}

	// On a cluster, pin both instances to the local member. Left to the
	// scheduler they can land on another node, where two things break the
	// assertions (not the plugin): exec output streamed through the local
	// unix socket from a remote member arrives empty/truncated, and the
	// test containers may get no network there. The plugin roundtrip
	// itself is cross-node capable (first live run proved it against an
	// instance living on another member).
	localMember := ""
	if srv.Environment.ServerClustered {
		localMember = srv.Environment.ServerName
		t.Logf("clustered server: pinning instances to member %q", localMember)
	}

	suffix := time.Now().Unix()
	srcName := fmt.Sprintf("plakar-e2e-src-%d", suffix)
	dstName := fmt.Sprintf("plakar-e2e-restored-%d", suffix)
	t.Cleanup(func() {
		deleteForce(server, srcName)
		deleteForce(server, dstName)
	})

	// --- Source instance: create, boot, plant fixtures. ---
	alias, imageServer := testImage()
	t.Logf("creating %s from %s (%s)", srcName, alias, imageServer)
	createServer := server
	if localMember != "" {
		createServer = server.UseTarget(localMember)
	}
	createOp, err := createServer.CreateInstance(api.InstancesPost{
		Name: srcName,
		Type: api.InstanceTypeContainer,
		Source: api.InstanceSource{
			Type:     "image",
			Alias:    alias,
			Server:   imageServer,
			Protocol: "simplestreams",
		},
	})
	if err != nil {
		t.Fatalf("create %s: %v", srcName, err)
	}
	if err := createOp.WaitContext(ctx); err != nil {
		t.Fatalf("create %s: %v", srcName, err)
	}
	startAndWait(ctx, t, server, srcName)

	// Fixtures covering the fidelity claims of the connector:
	// setuid bit + non-root uid/gid, a hardlink pair, a symlink and a
	// fifo, all under /root so they sit in stable rootfs territory.
	// chown BEFORE chmod: on Linux chown clears the setuid/setgid bits,
	// so the reverse order silently plants a 0755 fixture and the setuid
	// assertion after restore tests nothing.
	//
	// The final touch pins an integer-second mtime: a freshly written
	// file carries a sub-second mtime, and Incus's backup unpacking
	// rounds those up to the next full second (run 3: source 975.x
	// stat'ed as 975, restore as 976 — every image file, with
	// integer-second mtimes, round-tripped exactly). Pinning keeps the
	// manifest's mtime comparison meaningful without tripping on that
	// server-side rounding, which is outside the plugin's control.
	mustExec(ctx, t, server, srcName, `
		printf fixture-data > /root/fixture &&
		chown 1234:5678 /root/fixture &&
		chmod 4755 /root/fixture &&
		ln /root/fixture /root/fixture-hl &&
		ln -s ../root/fixture /root/fixture-sym &&
		mkfifo /root/fixture-fifo &&
		touch -d @1700000000 /root/fixture /root/fixture-fifo
	`)

	// File-capability fixture (security.capability xattr, what getcap
	// reads). setcap ships in Alpine's libcap tooling and needs network
	// to install, so this part is best-effort: without network the rest
	// of the roundtrip is still fully asserted.
	haveCaps := false
	capScript := `
		i=0; while [ $i -lt 5 ]; do
			apk add --no-cache libcap >/dev/null 2>&1 && break
			i=$((i+1)); sleep 2
		done
		command -v setcap >/dev/null 2>&1 || apk add --no-cache libcap-utils >/dev/null 2>&1
		command -v setcap >/dev/null 2>&1 || exit 42
		cp /bin/busybox /root/capbin &&
		setcap cap_net_raw+ep /root/capbin &&
		getcap /root/capbin
	`
	if out, code := execIn(ctx, t, server, srcName, capScript); code == 0 && strings.Contains(out, "cap_net_raw") {
		haveCaps = true
	} else {
		t.Logf("capability fixture unavailable (no network for apk?): rc=%d out=%q — skipping getcap assertion", code, out)
	}

	srcManifest := mustExec(ctx, t, server, srcName, manifestScript)

	// Backup of a stopped instance: quiescent rootfs, nothing drifts
	// between manifest collection and the tar stream.
	stopAndWait(ctx, t, server, srcName)

	// --- Roundtrip through the plugin code paths. ---
	t.Logf("backup %s -> restore %s via importer/exporter", srcName, dstName)
	roundtrip(ctx, t, srcName, dstName, localMember)

	// --- Restored instance: boot and assert. ---
	dst, _, err := server.GetInstance(dstName)
	if err != nil {
		t.Fatalf("restored instance %s not found: %v", dstName, err)
	}
	t.Logf("restored instance %s lives on member %q", dstName, dst.Location)
	startAndWait(ctx, t, server, dstName)

	// uid/gid/mode incl. setuid bit.
	if out := mustExec(ctx, t, server, dstName, `stat -c '%a %u %g' /root/fixture`); strings.TrimSpace(out) != "4755 1234 5678" {
		t.Errorf("fixture mode/owner: got %q, want \"4755 1234 5678\"", strings.TrimSpace(out))
	}

	// Hardlink pair: same inode, link count 2, same content.
	hl := mustExec(ctx, t, server, dstName, `
		a=$(stat -c '%i %h' /root/fixture)
		b=$(stat -c '%i %h' /root/fixture-hl)
		echo "$a|$b|$(cat /root/fixture)|$(cat /root/fixture-hl)"
	`)
	parts := strings.Split(strings.TrimSpace(hl), "|")
	if len(parts) != 4 || parts[0] != parts[1] || parts[2] != "fixture-data" || parts[3] != "fixture-data" {
		t.Errorf("hardlink pair broken after restore: %q", hl)
	}
	if !strings.HasSuffix(parts[0], " 2") {
		t.Errorf("hardlink count: got %q, want inode with 2 links", parts[0])
	}

	// Symlink target and fifo type.
	if out := mustExec(ctx, t, server, dstName, `readlink /root/fixture-sym`); strings.TrimSpace(out) != "../root/fixture" {
		t.Errorf("symlink target: got %q, want \"../root/fixture\"", strings.TrimSpace(out))
	}
	mustExec(ctx, t, server, dstName, `test -p /root/fixture-fifo`)

	// File capabilities survive (security.capability xattr).
	if haveCaps {
		if out := mustExec(ctx, t, server, dstName, `getcap /root/capbin`); !strings.Contains(out, "cap_net_raw") {
			t.Errorf("file capability lost on restore: getcap output %q", out)
		}
	}

	// Full-manifest comparison over the stable rootfs subtrees.
	dstManifest := mustExec(ctx, t, server, dstName, manifestScript)
	if diff := manifestDiff(srcManifest, dstManifest); diff != "" {
		t.Errorf("rootfs manifest differs between source and restore:\n%s", diff)
	}
}

// manifestScript prints one line per rootfs entry (type, mode, owner,
// and size+mtime for regular files) over the subtrees that do not
// change while an instance idles. /etc/hostname, resolv.conf and hosts
// are managed per-instance and legitimately differ. /etc/inittab is
// rewritten by Incus's image templates at instance creation, so it
// carries a fresh sub-second mtime that the backup pipeline rounds to
// the next second server-side (see the fixture touch above) — content
// still round-trips, only its mtime second is unstable.
const manifestScript = `
	find /bin /sbin /lib /usr /root /etc -xdev 2>/dev/null | sort | while read f; do
		case "$f" in /etc/hostname|/etc/resolv.conf|/etc/hosts|/etc/inittab) continue;; esac
		if [ -L "$f" ]; then echo "L $f -> $(readlink "$f")"
		elif [ -f "$f" ]; then echo "F $(stat -c '%a %u %g %s %Y' "$f") $f"
		elif [ -d "$f" ]; then echo "D $(stat -c '%a %u %g' "$f") $f"
		else echo "O $(stat -c '%a %u %g' "$f") $f"
		fi
	done
`

// manifestDiff returns a human-readable diff of the two manifests, empty
// when identical. Order-insensitive on top of the in-container sort.
func manifestDiff(src, dst string) string {
	index := func(s string) map[string]struct{} {
		m := make(map[string]struct{})
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				m[l] = struct{}{}
			}
		}
		return m
	}
	srcSet, dstSet := index(src), index(dst)
	var b strings.Builder
	report := func(prefix string, have, other map[string]struct{}) {
		n := 0
		for l := range have {
			if _, ok := other[l]; !ok {
				if n < 20 {
					fmt.Fprintf(&b, "%s %s\n", prefix, l)
				}
				n++
			}
		}
		if n > 20 {
			fmt.Fprintf(&b, "%s ... and %d more\n", prefix, n-20)
		}
	}
	report("- only in source: ", srcSet, dstSet)
	report("+ only in restore:", dstSet, srcSet)
	return b.String()
}

// roundtrip streams srcName through the plugin importer and pipes every
// record into the plugin exporter targeting dstName — the same record
// protocol kloset drives, minus the repository in the middle. kloset
// acks each importer record only after persisting its body, then
// replays records with repo-backed readers; the shim reproduces that
// contract by buffering each body before acking, so the importer's
// sequential tar reader is never invalidated while the exporter still
// holds the record pending.
func roundtrip(ctx context.Context, t *testing.T, srcName, dstName, targetMember string) {
	t.Helper()

	imp, err := pimporter.NewImporter(ctx, &connectors.Options{}, "incus",
		map[string]string{"location": "incus://" + srcName, "socket": socketPath})
	if err != nil {
		t.Fatalf("NewImporter: %v", err)
	}
	defer func() { _ = imp.Close(ctx) }()
	expConfig := map[string]string{"location": "incus://" + dstName, "socket": socketPath}
	if targetMember != "" {
		// Exercises the exporter's cluster `target` option, and keeps the
		// restored instance on the local member so the exec-based
		// assertions below see reliable output (see the clustered note in
		// the test body).
		expConfig["target"] = targetMember
	}
	exp, err := pexporter.NewExporter(ctx, &connectors.Options{}, "incus", expConfig)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	defer func() { _ = exp.Close(ctx) }()

	impRecords := make(chan *connectors.Record)
	impResults := make(chan *connectors.Result)
	expRecords := make(chan *connectors.Record)
	expResults := make(chan *connectors.Result)

	impDone := make(chan error, 1)
	go func() { impDone <- imp.Import(ctx, impRecords, impResults) }()
	expDone := make(chan error, 1)
	go func() { expDone <- exp.Export(ctx, expRecords, expResults) }()

	var expErrs []string
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for res := range expResults {
			if res.Err != nil {
				expErrs = append(expErrs, fmt.Sprintf("%s: %v", res.Record.Pathname, res.Err))
			}
		}
	}()

	for rec := range impRecords {
		fwd := *rec
		if rec.Reader != nil {
			data, err := io.ReadAll(rec.Reader)
			if err != nil {
				t.Fatalf("read record %s: %v", rec.Pathname, err)
			}
			fwd.Reader = io.NopCloser(bytes.NewReader(data))
		}
		expRecords <- &fwd
		impResults <- rec.Ok()
	}
	close(expRecords)

	if err := <-impDone; err != nil {
		t.Fatalf("import %s: %v", srcName, err)
	}
	if err := <-expDone; err != nil {
		t.Fatalf("export/restore %s: %v", dstName, err)
	}
	<-drained
	if len(expErrs) > 0 {
		t.Fatalf("exporter rejected %d record(s):\n%s", len(expErrs), strings.Join(expErrs, "\n"))
	}
}

// execIn runs a shell script in the instance and returns stdout+stderr
// and the exit code. Stdout and stderr MUST be distinct buffers: the
// incus client pumps them from two goroutines, and sharing one
// (non-thread-safe) bytes.Buffer corrupts or drops output at random —
// exactly what run 2 of the live E2E produced (empty multi-line
// results, manifests with ~25% of their lines missing).
func execIn(ctx context.Context, t *testing.T, server incus.InstanceServer, name, script string) (string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	dataDone := make(chan bool)
	op, err := server.ExecInstance(name, api.InstanceExecPost{
		Command:   []string{"/bin/sh", "-c", script},
		WaitForWS: true,
		Environment: map[string]string{
			"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"HOME": "/root",
		},
	}, &incus.InstanceExecArgs{Stdout: &stdout, Stderr: &stderr, DataDone: dataDone})
	if err != nil {
		t.Fatalf("exec in %s: %v", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		t.Fatalf("exec in %s: %v", name, err)
	}
	<-dataDone
	code, ok := op.Get().Metadata["return"].(float64)
	if !ok {
		t.Fatalf("exec in %s: no return code in operation metadata", name)
	}
	return stdout.String() + stderr.String(), int(code)
}

// mustExec is execIn failing the test on a non-zero exit code.
func mustExec(ctx context.Context, t *testing.T, server incus.InstanceServer, name, script string) string {
	t.Helper()
	out, code := execIn(ctx, t, server, name, script)
	if code != 0 {
		t.Fatalf("exec in %s: exit %d\nscript: %s\noutput: %s", name, code, script, out)
	}
	return out
}

func startAndWait(ctx context.Context, t *testing.T, server incus.InstanceServer, name string) {
	t.Helper()
	op, err := server.UpdateInstanceState(name, api.InstanceStatePut{Action: "start", Timeout: -1}, "")
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	// Running != exec-ready: poll until PID 1 answers.
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, code := execIn(ctx, t, server, name, "true"); code == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("instance %s did not become exec-ready within 90s", name)
		}
		time.Sleep(2 * time.Second)
	}
}

func stopAndWait(ctx context.Context, t *testing.T, server incus.InstanceServer, name string) {
	t.Helper()
	op, err := server.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Timeout: 30, Force: false}, "")
	if err != nil {
		t.Fatalf("stop %s: %v", name, err)
	}
	if err := op.WaitContext(ctx); err != nil {
		// Graceful shutdown timed out; a forced stop still gives a
		// consistent (if abrupt) rootfs for backup purposes.
		op, err = server.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Force: true}, "")
		if err != nil {
			t.Fatalf("force stop %s: %v", name, err)
		}
		if err := op.WaitContext(ctx); err != nil {
			t.Fatalf("force stop %s: %v", name, err)
		}
	}
}

// deleteForce is best-effort teardown: stop hard, delete, ignore errors
// (the instance may never have been created if the test failed early).
func deleteForce(server incus.InstanceServer, name string) {
	if op, err := server.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Force: true}, ""); err == nil {
		_ = op.Wait()
	}
	if op, err := server.DeleteInstance(name); err == nil {
		_ = op.Wait()
	}
}
