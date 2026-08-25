package connector

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

var testModTime = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

// tarEntry describes one entry of an archive as OpenWRT would produce it.
type tarEntry struct {
	name     string
	typeflag byte
	mode     int64
	link     string
	body     string
	uid, gid int
}

func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Mode:     e.mode,
			Linkname: e.link,
			ModTime:  testModTime,
			Uid:      e.uid,
			Gid:      e.gid,
		}
		if e.typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%s): %v", e.name, err)
		}
		if e.typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("Write(%s): %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(b); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestFinfo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hdr       tar.Header
		wantType  fs.FileMode
		wantPerm  fs.FileMode
		wantExtra fs.FileMode
	}{
		{
			name:     "regular",
			hdr:      tar.Header{Name: "etc/config/network", Typeflag: tar.TypeReg, Mode: 0644, Size: 12},
			wantPerm: 0644,
		},
		{
			name:      "setuid keeps the permission bits",
			hdr:       tar.Header{Name: "usr/bin/busybox", Typeflag: tar.TypeReg, Mode: 04755},
			wantPerm:  0755,
			wantExtra: fs.ModeSetuid,
		},
		{
			name:      "setgid",
			hdr:       tar.Header{Name: "etc/x", Typeflag: tar.TypeReg, Mode: 02640},
			wantPerm:  0640,
			wantExtra: fs.ModeSetgid,
		},
		{
			name:      "sticky",
			hdr:       tar.Header{Name: "tmp/x", Typeflag: tar.TypeReg, Mode: 01777},
			wantPerm:  0777,
			wantExtra: fs.ModeSticky,
		},
		{
			name:     "symlink",
			hdr:      tar.Header{Name: "etc/resolv.conf", Typeflag: tar.TypeSymlink, Mode: 0777, Linkname: "/tmp/resolv.conf"},
			wantType: fs.ModeSymlink,
			wantPerm: 0777,
		},
		{
			name:     "directory",
			hdr:      tar.Header{Name: "etc/config/", Typeflag: tar.TypeDir, Mode: 0755},
			wantType: fs.ModeDir,
			wantPerm: 0755,
		},
		{
			name:     "fifo",
			hdr:      tar.Header{Name: "etc/fifo", Typeflag: tar.TypeFifo, Mode: 0600},
			wantType: fs.ModeNamedPipe,
			wantPerm: 0600,
		},
		{
			// A char device is ModeDevice|ModeCharDevice in Go, setting only
			// ModeCharDevice would make tar.FileInfoHeader miss the type.
			name:     "char device",
			hdr:      tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Mode: 0666},
			wantType: fs.ModeDevice | fs.ModeCharDevice,
			wantPerm: 0666,
		},
		{
			name:     "block device",
			hdr:      tar.Header{Name: "dev/sda", Typeflag: tar.TypeBlock, Mode: 0660},
			wantType: fs.ModeDevice,
			wantPerm: 0660,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hdr := tc.hdr
			hdr.ModTime = testModTime
			hdr.Uid, hdr.Gid = 65534, 65533
			hdr.Uname, hdr.Gname = "nobody", "nogroup"

			fi := finfo(&hdr)

			if got := fi.Lmode.Type(); got != tc.wantType {
				t.Errorf("type bits = %v, want %v", got, tc.wantType)
			}
			if got := fi.Lmode.Perm(); got != tc.wantPerm {
				t.Errorf("perm = %04o, want %04o", got, tc.wantPerm)
			}
			extra := fi.Lmode & (fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
			if extra != tc.wantExtra {
				t.Errorf("setuid/setgid/sticky = %v, want %v", extra, tc.wantExtra)
			}
			if want := path.Base(tc.hdr.Name); fi.Lname != want {
				t.Errorf("Lname = %q, want %q", fi.Lname, want)
			}
			if fi.Luid != 65534 || fi.Lgid != 65533 {
				t.Errorf("uid/gid = %d/%d, want 65534/65533", fi.Luid, fi.Lgid)
			}
			if fi.Lusername != "nobody" || fi.Lgroupname != "nogroup" {
				t.Errorf("uname/gname = %q/%q", fi.Lusername, fi.Lgroupname)
			}
			if !fi.LmodTime.Equal(testModTime) {
				t.Errorf("modtime = %v, want %v", fi.LmodTime, testModTime)
			}
		})
	}
}

func TestLinkTarget(t *testing.T) {
	for _, tc := range []struct {
		typeflag byte
		linkname string
		want     string
	}{
		{tar.TypeSymlink, "/tmp/resolv.conf", "/tmp/resolv.conf"},
		{tar.TypeSymlink, "../tmp/localtime", "../tmp/localtime"},
		{tar.TypeReg, "", ""},
		{tar.TypeDir, "", ""},
		// A hard link is refused by Import, it must not leak a target.
		{tar.TypeLink, "etc/passwd", ""},
	} {
		hdr := &tar.Header{Typeflag: tc.typeflag, Linkname: tc.linkname}
		if got := linkTarget(hdr); got != tc.want {
			t.Errorf("linkTarget(type %c, %q) = %q, want %q",
				tc.typeflag, tc.linkname, got, tc.want)
		}
	}
}

// runExport feeds records through handleExportChan and returns the archive
// along with the results it emitted.
func runExport(t *testing.T, records []*connectors.Record) (*bytes.Buffer, []*connectors.Result, error) {
	t.Helper()

	in := make(chan *connectors.Record, len(records)+1)
	for _, r := range records {
		in <- r
	}
	close(in)

	out := make(chan *connectors.Result, len(records)+1)

	o := &openwrt{}
	archive, err := o.handleExportChan(context.Background(), in, out)
	close(out)

	var results []*connectors.Result
	for res := range out {
		results = append(results, res)
	}
	return archive, results, err
}

func recordFor(pathname, target string, fi objects.FileInfo, body string) *connectors.Record {
	return connectors.NewRecord(pathname, target, fi, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(body)), nil
	})
}

// TestExportRoundTrip is the regression test for the whole import/export
// pipeline: what the device produced must come back byte for byte.
func TestExportRoundTrip(t *testing.T) {
	source := []tarEntry{
		{name: "etc/config/network", typeflag: tar.TypeReg, mode: 0644, body: "config interface 'lan'"},
		{name: "etc/shadow", typeflag: tar.TypeReg, mode: 0600, body: "root:x:"},
		{name: "etc/dropbear/dropbear_rsa_host_key", typeflag: tar.TypeReg, mode: 04755, body: "key"},
		{name: "etc/sticky", typeflag: tar.TypeReg, mode: 01644, body: "s"},
		{name: "etc/resolv.conf", typeflag: tar.TypeSymlink, mode: 0777, link: "/tmp/resolv.conf"},
		{name: "etc/localtime", typeflag: tar.TypeSymlink, mode: 0777, link: "../tmp/localtime"},
		{name: "etc/fifo", typeflag: tar.TypeFifo, mode: 0600},
		{name: "etc/nobody", typeflag: tar.TypeReg, mode: 0640, body: "x", uid: 65534, gid: 65534},
	}

	// Import side: turn the device archive into records.
	var records []*connectors.Record
	tr := tar.NewReader(bytes.NewReader(buildTar(t, source)))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading source archive: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %s: %v", hdr.Name, err)
		}
		records = append(records,
			recordFor("/"+hdr.Name, linkTarget(hdr), finfo(hdr), string(body)))
	}

	// Export side.
	archive, results, err := runExport(t, records)
	if err != nil {
		t.Fatalf("handleExportChan: %v", err)
	}
	if len(results) != len(source) {
		t.Fatalf("got %d results, want %d", len(results), len(source))
	}
	for _, res := range results {
		if res.Err != nil {
			t.Errorf("%s: unexpected error result: %v", res.Record.Pathname, res.Err)
		}
	}

	// The exporter hands the archive to sysupgrade gzipped.
	gr, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatalf("the archive is not valid gzip: %v", err)
	}
	defer func() { _ = gr.Close() }()

	got := map[string]*tar.Header{}
	bodies := map[string]string{}
	rr := tar.NewReader(gr)
	for {
		hdr, err := rr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading rebuilt archive: %v", err)
		}
		body, err := io.ReadAll(rr)
		if err != nil {
			t.Fatalf("reading %s: %v", hdr.Name, err)
		}
		h := *hdr
		got[strings.TrimSuffix(hdr.Name, "/")] = &h
		bodies[strings.TrimSuffix(hdr.Name, "/")] = string(body)
	}

	if len(got) != len(source) {
		t.Fatalf("rebuilt archive has %d entries, want %d: %v", len(got), len(source), got)
	}

	for _, want := range source {
		hdr, ok := got[want.name]
		if !ok {
			// A lost path means the entry was flattened to its basename.
			t.Errorf("%s: missing from the rebuilt archive", want.name)
			continue
		}
		if hdr.Typeflag != want.typeflag {
			t.Errorf("%s: typeflag = %c, want %c", want.name, hdr.Typeflag, want.typeflag)
		}
		if hdr.Mode != want.mode {
			t.Errorf("%s: mode = %04o, want %04o", want.name, hdr.Mode, want.mode)
		}
		if hdr.Linkname != want.link {
			t.Errorf("%s: linkname = %q, want %q", want.name, hdr.Linkname, want.link)
		}
		if hdr.Uid != want.uid || hdr.Gid != want.gid {
			t.Errorf("%s: uid/gid = %d/%d, want %d/%d",
				want.name, hdr.Uid, hdr.Gid, want.uid, want.gid)
		}
		if bodies[want.name] != want.body {
			t.Errorf("%s: body = %q, want %q", want.name, bodies[want.name], want.body)
		}
	}
}

func TestExportSkipsDirectories(t *testing.T) {
	// OpenWRT does not put directory entries in its own archive, we rebuild
	// one that looks the same.
	dir := objects.FileInfo{Lname: "config", Lmode: 0755 | fs.ModeDir, LmodTime: testModTime}
	reg := objects.FileInfo{Lname: "network", Lsize: 3, Lmode: 0644, LmodTime: testModTime}

	archive, results, err := runExport(t, []*connectors.Record{
		recordFor("/", "", objects.FileInfo{Lname: "/", Lmode: 0700 | fs.ModeDir, LmodTime: testModTime}, ""),
		recordFor("/etc/config", "", dir, ""),
		recordFor("/etc/config/network", "", reg, "abc"),
	})
	if err != nil {
		t.Fatalf("handleExportChan: %v", err)
	}
	for _, res := range results {
		if res.Err != nil {
			t.Errorf("%s: unexpected error result: %v", res.Record.Pathname, res.Err)
		}
	}

	names := archiveNames(t, archive)
	if len(names) != 1 || names[0] != "etc/config/network" {
		t.Errorf("archive contains %v, want only etc/config/network", names)
	}
}

func archiveNames(t *testing.T, archive *bytes.Buffer) []string {
	t.Helper()

	gr, err := gzip.NewReader(bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer func() { _ = gr.Close() }()

	var names []string
	rr := tar.NewReader(gr)
	for {
		hdr, err := rr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if _, err := io.Copy(io.Discard, rr); err != nil {
			t.Fatalf("draining %s: %v", hdr.Name, err)
		}
		names = append(names, hdr.Name)
	}
}

// TestExportRejectsUnrepresentable checks we abort rather than upload an
// archive that does not describe what the snapshot contained.
func TestExportRejectsUnrepresentable(t *testing.T) {
	sock := objects.FileInfo{Lname: "sock", Lmode: 0600 | fs.ModeSocket, LmodTime: testModTime}

	_, results, err := runExport(t, []*connectors.Record{
		recordFor("/etc/sock", "", sock, ""),
	})
	if err == nil {
		t.Fatal("expected an error for a socket, got nil")
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("the record should have been reported in error, got %v", results)
	}
}

// TestExportRejectsMissingContent is the regression test for the silent
// truncation: a header announcing bytes we cannot provide used to poison the
// tar writer and drop every following entry.
func TestExportRejectsMissingContent(t *testing.T) {
	fi := objects.FileInfo{Lname: "big", Lsize: 4096, Lmode: 0644, LmodTime: testModTime}
	orphan := &connectors.Record{Pathname: "/etc/big", FileInfo: fi} // no Reader

	_, _, err := runExport(t, []*connectors.Record{orphan})
	if err == nil {
		t.Fatal("expected an error when no content is available, got nil")
	}
}

// fakeDevice serves the ubus login and the backup endpoints.
func fakeDevice(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"cafe"}]}`)
	})
	mux.HandleFunc("/cgi-bin/cgi-backup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="backup-openwrt.tar.gz"`)
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func importerFor(t *testing.T, srv *httptest.Server) *openwrt {
	t.Helper()

	host := strings.TrimPrefix(srv.URL, "http://")
	o, err := parseConfig(map[string]string{
		"location": "openwrt://" + host,
		"login":    "root",
		"use_ssl":  "false",
	})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	return o
}

// drainImport plays the role of the engine: it acknowledges every record and
// reads the content, like the backup loop does.
func drainImport(t *testing.T, o *openwrt) ([]*connectors.Record, map[string]string, error) {
	t.Helper()

	records := make(chan *connectors.Record, 32)
	results := make(chan *connectors.Result, 32)

	var seen []*connectors.Record
	bodies := map[string]string{}
	done := make(chan struct{})

	go func() {
		defer close(done)
		for rec := range records {
			if rec.Err == nil && rec.Reader != nil && rec.FileInfo.Lmode.IsRegular() {
				b, err := io.ReadAll(rec.Reader)
				if err != nil {
					t.Errorf("reading %s: %v", rec.Pathname, err)
				}
				bodies[rec.Pathname] = string(b)
			}
			seen = append(seen, rec)
			results <- rec.Ok()
		}
	}()

	err := o.Import(context.Background(), records, results)
	<-done
	return seen, bodies, err
}

func TestImport(t *testing.T) {
	source := []tarEntry{
		{name: "etc/config/network", typeflag: tar.TypeReg, mode: 0644, body: "config interface 'lan'"},
		{name: "etc/resolv.conf", typeflag: tar.TypeSymlink, mode: 0777, link: "/tmp/resolv.conf"},
	}
	srv := fakeDevice(t, gzipBytes(t, buildTar(t, source)))

	seen, bodies, err := drainImport(t, importerFor(t, srv))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("got %d records, want 2", len(seen))
	}

	if seen[0].Pathname != "/etc/config/network" {
		t.Errorf("pathname = %q, want /etc/config/network", seen[0].Pathname)
	}
	if got := bodies["/etc/config/network"]; got != "config interface 'lan'" {
		t.Errorf("body = %q", got)
	}

	if seen[1].Pathname != "/etc/resolv.conf" {
		t.Errorf("pathname = %q, want /etc/resolv.conf", seen[1].Pathname)
	}
	if seen[1].FileInfo.Lmode&fs.ModeSymlink == 0 {
		t.Errorf("resolv.conf should be a symlink, mode is %v", seen[1].FileInfo.Lmode)
	}
	if seen[1].Target != "/tmp/resolv.conf" {
		t.Errorf("target = %q, want /tmp/resolv.conf", seen[1].Target)
	}
}

func TestImportRefusesHardLinks(t *testing.T) {
	source := []tarEntry{
		{name: "etc/passwd", typeflag: tar.TypeReg, mode: 0644, body: "root:x:0:0"},
		{name: "etc/passwd-", typeflag: tar.TypeLink, mode: 0644, link: "etc/passwd"},
	}
	srv := fakeDevice(t, gzipBytes(t, buildTar(t, source)))

	seen, _, err := drainImport(t, importerFor(t, srv))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("got %d records, want 2", len(seen))
	}

	// The backup must not fail, the entry is reported and the rest goes on.
	if seen[0].Err != nil {
		t.Errorf("/etc/passwd should have been imported, got %v", seen[0].Err)
	}
	if seen[1].Err == nil {
		t.Fatal("the hard link should have been refused")
	}
	if seen[1].Pathname != "/etc/passwd-" {
		t.Errorf("pathname = %q, want /etc/passwd-", seen[1].Pathname)
	}
	if seen[1].Target != "" {
		t.Errorf("a refused record must not carry a target, got %q", seen[1].Target)
	}
}

func TestImportPropagatesDeviceError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"cafe"}]}`)
	})
	mux.HandleFunc("/cgi-bin/cgi-backup", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if _, _, err := drainImport(t, importerFor(t, srv)); err == nil {
		t.Fatal("expected an error when the device refuses the backup")
	}
}

func TestParseConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		o, err := parseConfig(map[string]string{
			"location": "openwrt://192.168.1.1",
			"login":    "root",
		})
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if o.baseURL != "https://192.168.1.1" {
			t.Errorf("baseURL = %q, want https://192.168.1.1", o.baseURL)
		}
		if o.host != "192.168.1.1" {
			t.Errorf("host = %q", o.host)
		}
		if !o.applySysupgrade || !o.rebootDevice {
			t.Errorf("apply_sysupgrade and reboot_device should default to true")
		}
	})

	t.Run("use_ssl false", func(t *testing.T) {
		o, err := parseConfig(map[string]string{
			"location": "openwrt://192.168.1.1:8080",
			"login":    "root",
			"use_ssl":  "false",
		})
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if o.baseURL != "http://192.168.1.1:8080" {
			t.Errorf("baseURL = %q", o.baseURL)
		}
	})

	for _, tc := range []struct {
		name   string
		config map[string]string
	}{
		{"missing location", map[string]string{"login": "root"}},
		{"missing login", map[string]string{"location": "openwrt://h"}},
		{"no scheme", map[string]string{"location": "192.168.1.1", "login": "root"}},
		{"bad use_ssl", map[string]string{"location": "openwrt://h", "login": "root", "use_ssl": "yes please"}},
		{"bad timeout", map[string]string{"location": "openwrt://h", "login": "root", "timeout": "soon"}},
		{"zero timeout", map[string]string{"location": "openwrt://h", "login": "root", "timeout": "0"}},
		{"negative timeout", map[string]string{"location": "openwrt://h", "login": "root", "timeout": "-1"}},
		{"bad insecure_skip_verify", map[string]string{"location": "openwrt://h", "login": "root", "insecure_skip_verify": "maybe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseConfig(tc.config); err == nil {
				t.Errorf("expected an error, got nil")
			}
		})
	}
}

func TestOrigin(t *testing.T) {
	o, err := parseConfig(map[string]string{
		"location": "openwrt://router.lan:8443",
		"login":    "root",
	})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if o.Origin() != "router.lan:8443" {
		t.Errorf("Origin() = %q, want router.lan:8443", o.Origin())
	}
	if o.Root() != "/" {
		t.Errorf("Root() = %q, want /", o.Root())
	}
}
