package exporter

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

// A broken remote config (url without the client certificate pair, or
// TLS options without url) must fail NewExporter with the config error,
// before any connection is attempted.
func TestNewExporterRejectsBadRemoteConfig(t *testing.T) {
	for _, cfg := range []map[string]string{
		{"location": "incus://web", "url": "https://incus.example:8443"},
		{"location": "incus://web", "tls_client_cert": "/some/cert.pem"},
	} {
		if _, err := NewExporter(context.Background(), &connectors.Options{}, "incus", cfg); err == nil {
			t.Fatalf("config %v: want error, got nil", cfg)
		} else if !strings.Contains(err.Error(), "tls_client_cert") {
			t.Fatalf("config %v: error should name the tls option, got %v", cfg, err)
		}
	}
}

type fakeSink struct {
	instance string
	tarball  bytes.Buffer
}

func (f *fakeSink) Ping(ctx context.Context) error { return nil }

func (f *fakeSink) Restore(ctx context.Context, instance string, tarStream io.Reader) error {
	f.instance = instance
	_, err := io.Copy(&f.tarball, tarStream)
	return err
}

func TestExportRebuildsTar(t *testing.T) {
	sink := &fakeSink{}
	exp := newExporterWithSink("restored-1", sink)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result, 8)
	done := make(chan error, 1)
	go func() { done <- exp.Export(context.Background(), records, results) }()

	feed := []*connectors.Record{
		{Pathname: "/backup/index.yaml",
			FileInfo: objects.FileInfo{Lname: "index.yaml", Lsize: 11, Lmode: 0644, LmodTime: time.Unix(1750000000, 0)},
			Reader:   io.NopCloser(strings.NewReader("name: test\n"))},
		{Pathname: "/backup/container/rootfs/etc",
			FileInfo: objects.FileInfo{Lname: "etc", Lmode: fs.ModeDir | 0755}},
		{Pathname: "/backup/container/rootfs/etc/hostname",
			FileInfo: objects.FileInfo{Lname: "hostname", Lsize: 5, Lmode: 0644},
			Reader:   io.NopCloser(strings.NewReader("test\n"))},
		{Pathname: "/backup/container/rootfs/bin", Target: "usr/bin",
			FileInfo: objects.FileInfo{Lname: "bin", Lmode: fs.ModeSymlink | 0777}},
	}
	for _, r := range feed {
		records <- r
	}
	close(records)
	if err := <-done; err != nil {
		t.Fatalf("Export: %v", err)
	}

	if sink.instance != "restored-1" {
		t.Fatalf("instance: %q", sink.instance)
	}

	// results: un ack par record (le contenu de Result est opaque au SDK ;
	// l'erreur globale d'Export est le vrai signal)
	for range len(feed) {
		<-results
	}

	// re-lire le tar produit
	tr := tar.NewReader(bytes.NewReader(sink.tarball.Bytes()))
	var names []string
	var hostname string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		if hdr.Name == "backup/container/rootfs/etc/hostname" {
			b, _ := io.ReadAll(tr)
			hostname = string(b)
		}
		if hdr.Name == "backup/container/rootfs/bin" && hdr.Linkname != "usr/bin" {
			t.Fatalf("symlink lost: %+v", hdr)
		}
	}
	want := []string{"backup/index.yaml", "backup/container/rootfs/etc/", "backup/container/rootfs/etc/hostname", "backup/container/rootfs/bin"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("entries: %v", names)
	}
	if hostname != "test\n" {
		t.Fatalf("hostname content: %q", hostname)
	}
}

// earlyFailSink emulates a real Incus rejecting an upload: it reads only a
// small prefix of the tar stream (e.g. just enough to inspect a header or
// fail an early sanity check) and returns without draining the rest.
type earlyFailSink struct {
	readN int64
	err   error
}

func (s *earlyFailSink) Ping(ctx context.Context) error { return nil }

func (s *earlyFailSink) Restore(ctx context.Context, instance string, tarStream io.Reader) error {
	_, _ = io.CopyN(io.Discard, tarStream, s.readN)
	return s.err
}

// TestExportSinkFailsEarly reproduces the deadlock found in review: when the
// restore sink returns before fully draining the tar stream, the io.Pipe
// writer side must be unblocked (via pr.CloseWithError in the sink
// goroutine), otherwise Export blocks forever on a pending pipe Write.
func TestExportSinkFailsEarly(t *testing.T) {
	wantErr := errors.New("sink rejected upload")
	sink := &earlyFailSink{readN: 512, err: wantErr}
	exp := newExporterWithSink("restored-2", sink)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result, 16)
	done := make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		err := exp.Export(context.Background(), records, results)
		done <- err
		close(stop)
	}()

	// Records large enough (128KiB each) to guarantee that, once the sink
	// stops reading, a pending body write blocks on the (unbuffered)
	// io.Pipe instead of completing instantly.
	zeros := make([]byte, 128*1024)
	feed := []*connectors.Record{
		{Pathname: "/backup/container/rootfs/big1",
			FileInfo: objects.FileInfo{Lname: "big1", Lsize: int64(len(zeros)), Lmode: 0644},
			Reader:   io.NopCloser(bytes.NewReader(zeros))},
		{Pathname: "/backup/container/rootfs/big2",
			FileInfo: objects.FileInfo{Lname: "big2", Lsize: int64(len(zeros)), Lmode: 0644},
			Reader:   io.NopCloser(bytes.NewReader(zeros))},
		{Pathname: "/backup/container/rootfs/big3",
			FileInfo: objects.FileInfo{Lname: "big3", Lsize: int64(len(zeros)), Lmode: 0644},
			Reader:   io.NopCloser(bytes.NewReader(zeros))},
	}

	sent := 0
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		for _, r := range feed {
			select {
			case records <- r:
				sent++
			case <-stop:
				// Export gave up (already returned): stop feeding
				// instead of blocking forever on a channel nobody
				// reads from anymore.
				return
			}
		}
		close(records)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Export: expected a non-nil error, got nil")
		}
		if !errors.Is(err, wantErr) && !strings.Contains(err.Error(), wantErr.Error()) {
			t.Fatalf("Export error = %v, want it to contain %v", err, wantErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Export deadlocked: a sink failing early must not block forever")
	}

	select {
	case <-feedDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the feeder goroutine to unblock")
	}

	got := 0
	for range results {
		got++
	}
	if got != sent {
		t.Fatalf("results delivered = %d, want %d (one per record actually read by Export)", got, sent)
	}
	if sent == 0 {
		t.Fatal("no record was ever handed to Export; test is not exercising the deadlock path")
	}
}

// TestExportNilReaderRegular ensures a malformed record (a regular file
// announcing a non-zero size but carrying no Reader) is rejected cleanly
// instead of writing a tar header promising a body that never arrives -
// which would silently corrupt every entry written after it.
func TestExportNilReaderRegular(t *testing.T) {
	sink := &fakeSink{}
	exp := newExporterWithSink("restored-3", sink)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result, 8)
	done := make(chan error, 1)
	go func() { done <- exp.Export(context.Background(), records, results) }()

	feed := []*connectors.Record{
		{Pathname: "/backup/index.yaml",
			FileInfo: objects.FileInfo{Lname: "index.yaml", Lsize: 11, Lmode: 0644},
			Reader:   io.NopCloser(strings.NewReader("name: test\n"))},
		{Pathname: "/backup/container/rootfs/broken",
			FileInfo: objects.FileInfo{Lname: "broken", Lsize: 42, Lmode: 0644},
			Reader:   nil},
		{Pathname: "/backup/container/rootfs/etc/hostname",
			FileInfo: objects.FileInfo{Lname: "hostname", Lsize: 5, Lmode: 0644},
			Reader:   io.NopCloser(strings.NewReader("test\n"))},
	}
	for _, r := range feed {
		records <- r
	}
	close(records)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Export: unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Export deadlocked")
	}

	var gotErr, gotOk int
	for range len(feed) {
		res := <-results
		if res.Record.Pathname == "/backup/container/rootfs/broken" {
			if res.Err == nil {
				t.Fatal("broken record: expected an Error result, got Ok")
			}
			gotErr++
			continue
		}
		if res.Err != nil {
			t.Fatalf("record %q: unexpected error result: %v", res.Record.Pathname, res.Err)
		}
		gotOk++
	}
	if gotErr != 1 || gotOk != 2 {
		t.Fatalf("results: gotErr=%d gotOk=%d, want 1 and 2", gotErr, gotOk)
	}

	tr := tar.NewReader(bytes.NewReader(sink.tarball.Bytes()))
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
	want := []string{"backup/index.yaml", "backup/container/rootfs/etc/hostname"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("entries: %v, want %v (broken record must not appear)", names, want)
	}
}

// TestExportShortReadDetected covers audit finding "Mineurs" #3: a record
// whose Reader yields fewer bytes than its declared FileInfo.Lsize must be
// reported as a clear, actionable error (naming the pathname and both byte
// counts) instead of silently producing a truncated tar entry that would
// only surface as a cryptic failure on a later WriteHeader call.
func TestExportShortReadDetected(t *testing.T) {
	sink := &fakeSink{}
	exp := newExporterWithSink("restored-6", sink)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result, 8)
	done := make(chan error, 1)
	go func() { done <- exp.Export(context.Background(), records, results) }()

	feed := []*connectors.Record{
		{Pathname: "/backup/container/rootfs/short",
			FileInfo: objects.FileInfo{Lname: "short", Lsize: 10, Lmode: 0644},
			Reader:   io.NopCloser(strings.NewReader("shrt"))}, // 4 bytes, declared 10
	}
	for _, r := range feed {
		records <- r
	}
	close(records)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Export: expected a non-nil error, got nil")
		}
		if !strings.Contains(err.Error(), "/backup/container/rootfs/short") ||
			!strings.Contains(err.Error(), "got 4 bytes") ||
			!strings.Contains(err.Error(), "want 10") {
			t.Fatalf("Export error = %q, want it to mention the pathname, got count and want count", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Export deadlocked")
	}

	res := <-results
	if res.Err == nil {
		t.Fatal("short-read record: expected an Error result, got Ok")
	}
}

// TestExportReinjectsXattrIntoPAXRecords covers audit finding #2: an
// xattr record fed in protocol order (immediately after its owning file
// record, same Pathname - see incus.go's Export doc comment) must be
// folded into that file's tar header PAXRecords, byte-identical to the
// value carried by the record's Reader.
func TestExportReinjectsXattrIntoPAXRecords(t *testing.T) {
	sink := &fakeSink{}
	exp := newExporterWithSink("restored-4", sink)

	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result, 8)
	done := make(chan error, 1)
	go func() { done <- exp.Export(context.Background(), records, results) }()

	capability := "\x01\x00\x00\x02\x00\x20\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"
	feed := []*connectors.Record{
		{Pathname: "/backup/container/rootfs/bin/ping",
			FileInfo: objects.FileInfo{Lname: "ping", Lsize: 4, Lmode: 0755},
			Reader:   io.NopCloser(strings.NewReader("elf\n"))},
		{Pathname: "/backup/container/rootfs/bin/ping",
			IsXattr:   true,
			XattrName: "security.capability",
			XattrType: objects.AttributeExtended,
			Reader:    io.NopCloser(strings.NewReader(capability))},
	}
	for _, r := range feed {
		records <- r
	}
	close(records)
	if err := <-done; err != nil {
		t.Fatalf("Export: %v", err)
	}
	for range len(feed) {
		<-results
	}

	tr := tar.NewReader(bytes.NewReader(sink.tarball.Bytes()))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "backup/container/rootfs/bin/ping" {
		t.Fatalf("entry name: %q", hdr.Name)
	}
	got, ok := hdr.PAXRecords["SCHILY.xattr.security.capability"]
	if !ok {
		t.Fatalf("PAXRecords missing SCHILY.xattr.security.capability: %+v", hdr.PAXRecords)
	}
	if got != capability {
		t.Fatalf("xattr value: got %q, want %q", got, capability)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("want a single tar entry (xattr record must not appear as its own entry), got err=%v", err)
	}
}

// TestRoundtripXattrSurvivesImportExport is the end-to-end sanity check
// requested for audit finding #2: a tar with a PAX xattr, run through
// the importer to produce records, then those records run through the
// exporter, must reproduce the same PAXRecords in the rebuilt tar.
func TestRoundtripXattrSurvivesImportExport(t *testing.T) {
	// Build the source tar the same way the importer test does, kept
	// self-contained here to avoid an import-package dependency.
	var srcBuf bytes.Buffer
	stw := tar.NewWriter(&srcBuf)
	capability := "\x01\x00\x00\x02\x00\x20\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"
	body := "#!/bin/true\n"
	srcHdr := tar.Header{
		Name:     "backup/container/rootfs/bin/ping",
		Mode:     0755,
		Typeflag: tar.TypeReg,
		Size:     int64(len(body)),
		PAXRecords: map[string]string{
			"SCHILY.xattr.security.capability": capability,
		},
	}
	if err := stw.WriteHeader(&srcHdr); err != nil {
		t.Fatal(err)
	}
	if _, err := stw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := stw.Close(); err != nil {
		t.Fatal(err)
	}

	// Read it back as plain tar records + PAX-derived xattr record, the
	// same shape the incus importer produces (see importer/incus.go).
	str := tar.NewReader(bytes.NewReader(srcBuf.Bytes()))
	srcHdrRead, err := str.Next()
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, err := io.ReadAll(str)
	if err != nil {
		t.Fatal(err)
	}

	sink := &fakeSink{}
	exp := newExporterWithSink("restored-5", sink)
	records := make(chan *connectors.Record)
	results := make(chan *connectors.Result, 8)
	done := make(chan error, 1)
	go func() { done <- exp.Export(context.Background(), records, results) }()

	feed := []*connectors.Record{
		{Pathname: "/" + srcHdrRead.Name,
			FileInfo: objects.FileInfo{Lname: "ping", Lsize: int64(len(bodyBytes)), Lmode: 0755},
			Reader:   io.NopCloser(bytes.NewReader(bodyBytes))},
		{Pathname: "/" + srcHdrRead.Name,
			IsXattr:   true,
			XattrName: "security.capability",
			XattrType: objects.AttributeExtended,
			Reader:    io.NopCloser(strings.NewReader(srcHdrRead.PAXRecords["SCHILY.xattr.security.capability"]))},
	}
	for _, r := range feed {
		records <- r
	}
	close(records)
	if err := <-done; err != nil {
		t.Fatalf("Export: %v", err)
	}
	for range len(feed) {
		<-results
	}

	outTr := tar.NewReader(bytes.NewReader(sink.tarball.Bytes()))
	outHdr, err := outTr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if outHdr.PAXRecords["SCHILY.xattr.security.capability"] != capability {
		t.Fatalf("roundtrip xattr value: got %q, want %q",
			outHdr.PAXRecords["SCHILY.xattr.security.capability"], capability)
	}
}
