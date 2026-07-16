package importer

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

type fakeSource struct {
	tarball    []byte
	cleaned    bool
	pinged     bool
	opened     bool
	instType   string
	extraDisks []string
	inspectErr error
}

func (f *fakeSource) Ping(ctx context.Context) error { f.pinged = true; return nil }

func (f *fakeSource) Inspect(ctx context.Context, instance string) (instanceInfo, error) {
	if f.inspectErr != nil {
		return instanceInfo{}, f.inspectErr
	}
	return instanceInfo{Type: f.instType, ExtraDisks: f.extraDisks}, nil
}

func (f *fakeSource) Open(ctx context.Context, instance string) (io.ReadCloser, func() error, error) {
	f.opened = true
	return io.NopCloser(bytes.NewReader(f.tarball)), func() error { f.cleaned = true; return nil }, nil
}

// makeTar builds an incus-export-shaped tarball in memory.
func makeTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entries := []struct {
		hdr  tar.Header
		body string
	}{
		{tar.Header{Name: "backup/index.yaml", Mode: 0644, Typeflag: tar.TypeReg, Size: 12}, "name: test\n_"},
		{tar.Header{Name: "backup/container/rootfs/etc/", Mode: 0755, Typeflag: tar.TypeDir}, ""},
		{tar.Header{Name: "backup/container/rootfs/etc/hostname", Mode: 0644, Typeflag: tar.TypeReg, Size: 5}, "test\n"},
		{tar.Header{Name: "backup/container/rootfs/bin", Linkname: "usr/bin", Typeflag: tar.TypeSymlink}, ""},
	}
	for _, e := range entries {
		if err := tw.WriteHeader(&e.hdr); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportEmitsRecords(t *testing.T) {
	src := &fakeSource{tarball: makeTar(t)}
	imp := newImporterWithSource("test", src)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	done := make(chan error, 1)
	go func() { done <- imp.Import(context.Background(), records, results) }()

	var got []*connectors.Record
	var contents []string
	for rec := range records {
		got = append(got, rec)
		if rec.Reader != nil && rec.FileInfo.Lmode.IsRegular() {
			b, err := io.ReadAll(rec.Reader)
			if err != nil {
				t.Fatal(err)
			}
			contents = append(contents, string(b))
		}
		results <- rec.Ok() // ack, comme le fait kloset
	}
	if err := <-done; err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("want 4 records, got %d", len(got))
	}
	if got[0].Pathname != "/backup/index.yaml" {
		t.Fatalf("first record: %q", got[0].Pathname)
	}
	if got[3].Target != "usr/bin" {
		t.Fatalf("symlink target: %q", got[3].Target)
	}
	if len(contents) != 2 || contents[1] != "test\n" {
		t.Fatalf("contents: %q", contents)
	}
	if !src.cleaned {
		t.Fatal("incus backup not cleaned up")
	}
}

// drainImport runs Import to completion, acking every record, and
// returns Import's error.
func drainImport(t *testing.T, imp *Importer) error {
	t.Helper()
	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	done := make(chan error, 1)
	go func() { done <- imp.Import(context.Background(), records, results) }()
	for rec := range records {
		results <- rec.Ok()
	}
	return <-done
}

// TestImportWarnsAboutExtraDisks covers TODO-PROD #4: attached disk
// devices beyond the root filesystem (custom volumes, host mounts) are
// excluded by InstanceOnly:true, so the backup must say so loudly.
func TestImportWarnsAboutExtraDisks(t *testing.T) {
	src := &fakeSource{tarball: makeTar(t), extraDisks: []string{"data", "logs"}}
	imp := newImporterWithSource("test", src)
	var stderr bytes.Buffer
	imp.stderr = &stderr

	if err := drainImport(t, imp); err != nil {
		t.Fatalf("Import: %v", err)
	}

	out := stderr.String()
	for _, want := range []string{`disk device "data"`, `disk device "logs"`, "NOT included"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stderr missing %q:\n%s", want, out)
		}
	}
}

// A detection failure must not fail the backup, only leave a note.
func TestImportExtraDiskDetectionFailureIsNonFatal(t *testing.T) {
	src := &fakeSource{tarball: makeTar(t), inspectErr: errors.New("boom")}
	imp := newImporterWithSource("test", src)
	var stderr bytes.Buffer
	imp.stderr = &stderr

	if err := drainImport(t, imp); err != nil {
		t.Fatalf("Import must succeed despite detection failure: %v", err)
	}
	if !strings.Contains(stderr.String(), "could not inspect instance") {
		t.Fatalf("stderr missing detection-failure note:\n%s", stderr.String())
	}
	if !src.cleaned {
		t.Fatal("incus backup not cleaned up")
	}
}

// TestImportRefusesVirtualMachine covers TODO-PROD #5: a VM export is a
// single monolithic disk image inside the tar — per-file browsing and
// dedup are meaningless on it and the path is untested — so the importer
// must refuse VMs with a clear error before creating any server-side
// backup, instead of silently producing a useless snapshot.
func TestImportRefusesVirtualMachine(t *testing.T) {
	src := &fakeSource{tarball: makeTar(t), instType: "virtual-machine"}
	imp := newImporterWithSource("vm-1", src)

	err := drainImport(t, imp)
	if err == nil {
		t.Fatal("Import of a virtual-machine must fail")
	}
	for _, want := range []string{"vm-1", "virtual-machine", "containers"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must mention %q, got: %v", want, err)
		}
	}
	if src.opened {
		t.Fatal("no server-side backup must be created for a refused VM")
	}
}

// Explicit "container" and empty type (legacy servers predating the
// Type field) must both pass the VM gate.
func TestImportAcceptsContainerTypes(t *testing.T) {
	for _, typ := range []string{"container", ""} {
		src := &fakeSource{tarball: makeTar(t), instType: typ}
		imp := newImporterWithSource("test", src)
		if err := drainImport(t, imp); err != nil {
			t.Fatalf("type %q: Import: %v", typ, err)
		}
	}
}

func TestExtraDiskDevices(t *testing.T) {
	devices := map[string]map[string]string{
		"root":  {"type": "disk", "path": "/", "pool": "default"},
		"data":  {"type": "disk", "path": "/srv/data", "pool": "default", "source": "vol-data"},
		"cache": {"type": "disk", "path": "/var/cache", "source": "/host/cache"},
		"eth0":  {"type": "nic", "network": "incusbr0"},
		// Regenerated from config at start: no data to lose, must
		// not be warned about (TODO-PROD #4b).
		"cloud-init": {"type": "disk", "source": "cloud-init:config"},
		"agent":      {"type": "disk", "source": "agent:config"},
	}
	got := extraDiskDevices(devices)
	if len(got) != 2 || got[0] != "cache" || got[1] != "data" {
		t.Fatalf("extraDiskDevices: %v", got)
	}
	if got := extraDiskDevices(nil); len(got) != 0 {
		t.Fatalf("nil devices: %v", got)
	}
}

// TestOriginQualifiedByProject covers TODO-PROD #3b: instance names are
// only unique within a project, so a non-default project must qualify
// the snapshot origin; without a project the origin stays the bare
// instance name (existing snapshots keep their naming).
func TestOriginQualifiedByProject(t *testing.T) {
	imp := newImporterWithSource("web-1", &fakeSource{})
	if got := imp.Origin(); got != "web-1" {
		t.Fatalf("origin without project: got %q, want %q", got, "web-1")
	}
	imp.project = "customer-a"
	if got := imp.Origin(); got != "customer-a/web-1" {
		t.Fatalf("origin with project: got %q, want %q", got, "customer-a/web-1")
	}
}

// TestWrapBackupError covers TODO-PROD #6: CreateInstanceBackup
// materializes the whole tarball on the server before we can stream
// it, so a "no space left" failure there must come back with guidance
// (where the space is needed, how to move it), not just the raw Incus
// error. Any other failure must stay untouched: no misleading disk
// advice on unrelated errors.
func TestWrapBackupError(t *testing.T) {
	for _, raw := range []string{
		"write /var/lib/incus/backups/instances/web-1: no space left on device",
		"Failed to write backup file: Disk quota exceeded",
	} {
		base := errors.New(raw)
		err := wrapBackupError("web-1", base)
		if !errors.Is(err, base) {
			t.Fatalf("%q: must wrap the original error, got %v", raw, err)
		}
		for _, want := range []string{"web-1", "storage.backups_volume", "/var/lib/incus/backups"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%q: enriched error must mention %q, got %v", raw, want, err)
			}
		}
	}

	plain := errors.New("Instance not found")
	err := wrapBackupError("web-1", plain)
	if !errors.Is(err, plain) {
		t.Fatalf("must wrap the original error, got %v", err)
	}
	if strings.Contains(err.Error(), "storage.backups_volume") {
		t.Fatalf("unrelated error must not get disk-space advice, got %v", err)
	}
	if !strings.Contains(err.Error(), "web-1") {
		t.Fatalf("error must name the instance, got %v", err)
	}
}

// TestDurationOption covers TODO-PROD #12: the backup TTL and cleanup
// timeout must be configurable, with the historical hardcoded values as
// defaults, and garbage or non-positive durations rejected up front.
func TestDurationOption(t *testing.T) {
	def := 6 * time.Hour

	got, err := durationOption(map[string]string{}, "backup_ttl", def)
	if err != nil || got != def {
		t.Fatalf("absent key: got %v, %v; want %v, nil", got, err, def)
	}

	got, err = durationOption(map[string]string{"backup_ttl": "30m"}, "backup_ttl", def)
	if err != nil || got != 30*time.Minute {
		t.Fatalf("valid value: got %v, %v; want 30m, nil", got, err)
	}

	for _, bad := range []string{"bogus", "0", "-1h", "0s"} {
		if _, err := durationOption(map[string]string{"backup_ttl": bad}, "backup_ttl", def); err == nil {
			t.Fatalf("value %q: want error, got nil", bad)
		} else if !strings.Contains(err.Error(), "backup_ttl") {
			t.Fatalf("value %q: error must name the option, got %v", bad, err)
		}
	}
}

// A bad duration must fail NewImporter before any daemon connection is
// attempted, so the user sees the config error rather than a dial error.
func TestNewImporterRejectsInvalidTimeouts(t *testing.T) {
	for _, cfg := range []map[string]string{
		{"location": "incus://web", "backup_ttl": "bogus"},
		{"location": "incus://web", "cleanup_timeout": "-1m"},
	} {
		if _, err := NewImporter(context.Background(), &connectors.Options{}, "incus", cfg); err == nil {
			t.Fatalf("config %v: want error, got nil", cfg)
		}
	}
}

// A broken remote config (url without the client certificate pair, or
// TLS options without url) must fail NewImporter with the config error,
// before any connection is attempted.
func TestNewImporterRejectsBadRemoteConfig(t *testing.T) {
	for _, cfg := range []map[string]string{
		{"location": "incus://web", "url": "https://incus.example:8443"},
		{"location": "incus://web", "tls_client_cert": "/some/cert.pem"},
	} {
		if _, err := NewImporter(context.Background(), &connectors.Options{}, "incus", cfg); err == nil {
			t.Fatalf("config %v: want error, got nil", cfg)
		} else if !strings.Contains(err.Error(), "tls_client_cert") {
			t.Fatalf("config %v: error should name the tls option, got %v", cfg, err)
		}
	}
}

// makeTarWithXattr builds a single-file tarball whose entry carries a
// binary-ish PAX xattr record, as GNU/BSD tar would encode
// security.capability.
func makeTarWithXattr(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := "#!/bin/true\n"
	hdr := tar.Header{
		Name:     "backup/container/rootfs/bin/ping",
		Mode:     0755,
		Typeflag: tar.TypeReg,
		Size:     int64(len(body)),
		PAXRecords: map[string]string{
			"SCHILY.xattr.security.capability": "\x01\x00\x00\x02\x00\x20\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00",
		},
	}
	if err := tw.WriteHeader(&hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestImportEmitsXattrRecord covers audit finding #2: a PAX
// "SCHILY.xattr.*" record on a tar entry must be replayed as its own
// kloset xattr Record, immediately after the owning file record and
// sharing its Pathname (see emitXattrs's doc comment for why this
// order is load-bearing).
func TestImportEmitsXattrRecord(t *testing.T) {
	src := &fakeSource{tarball: makeTarWithXattr(t)}
	imp := newImporterWithSource("test", src)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result)
	done := make(chan error, 1)
	go func() { done <- imp.Import(context.Background(), records, results) }()

	var got []*connectors.Record
	for rec := range records {
		got = append(got, rec)
		results <- rec.Ok()
	}
	if err := <-done; err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("want 2 records (file + xattr), got %d", len(got))
	}

	file, xattr := got[0], got[1]
	if file.IsXattr {
		t.Fatalf("first record must be the file record, got IsXattr=true: %+v", file)
	}
	if !xattr.IsXattr {
		t.Fatalf("second record must be the xattr record, got IsXattr=false: %+v", xattr)
	}
	if xattr.Pathname != file.Pathname {
		t.Fatalf("xattr record pathname %q != file record pathname %q", xattr.Pathname, file.Pathname)
	}
	if want := "/backup/container/rootfs/bin/ping"; file.Pathname != want {
		t.Fatalf("file pathname: got %q, want %q", file.Pathname, want)
	}
	if xattr.XattrName != "security.capability" {
		t.Fatalf("xattr name: got %q, want %q", xattr.XattrName, "security.capability")
	}
	if xattr.XattrType != objects.AttributeExtended {
		t.Fatalf("xattr type: got %v, want AttributeExtended", xattr.XattrType)
	}
	value, err := io.ReadAll(xattr.Reader)
	if err != nil {
		t.Fatal(err)
	}
	want := "\x01\x00\x00\x02\x00\x20\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"
	if string(value) != want {
		t.Fatalf("xattr value: got %q, want %q", value, want)
	}
}
