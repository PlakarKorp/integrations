package integration_grpc

import (
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
)

func sampleFileInfo() objects.FileInfo {
	return objects.FileInfo{
		Lname:      "hello.txt",
		Lsize:      1234,
		Lmode:      0o644,
		LmodTime:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Ldev:       1,
		Lino:       2,
		Luid:       1000,
		Lgid:       1000,
		Lnlink:     1,
		Lusername:  "alice",
		Lgroupname: "users",
		Flags:      0x42,
	}
}

func TestRecordRoundTrip_Regular(t *testing.T) {
	rec := &connectors.Record{
		Pathname: "/etc/hello.txt",
		FileInfo: sampleFileInfo(),
		Target:   "",
		ExtendedAttributes: []string{"user.foo", "user.bar"},
		FileAttributes:     0xdead,
		Reader:             io.NopCloser(strings.NewReader("xx")),
	}

	pb := RecordToProto(rec)
	if !pb.HasReader {
		t.Fatalf("regular file with reader should have HasReader=true")
	}

	got, err := RecordFromProto(pb)
	if err != nil {
		t.Fatalf("RecordFromProto: %v", err)
	}
	if got.Pathname != rec.Pathname {
		t.Errorf("pathname mismatch: %q vs %q", got.Pathname, rec.Pathname)
	}
	if !got.FileInfo.Equal(&rec.FileInfo) {
		t.Errorf("fileinfo mismatch: %+v vs %+v", got.FileInfo, rec.FileInfo)
	}
	if got.FileAttributes != rec.FileAttributes {
		t.Errorf("fileattributes mismatch")
	}
}

func TestRecordRoundTrip_ErrorSkipsFileInfo(t *testing.T) {
	rec := &connectors.Record{
		Pathname: "/missing",
		Err:      errors.New("ENOENT"),
	}

	pb := RecordToProto(rec)
	if pb.FileInfo != nil {
		t.Errorf("error record should not have FileInfo serialised")
	}
	if pb.HasReader {
		t.Errorf("error record should not advertise HasReader")
	}
	if pb.Error != "ENOENT" {
		t.Errorf("error string lost: %q", pb.Error)
	}

	got, err := RecordFromProto(pb)
	if err != nil {
		t.Fatalf("RecordFromProto: %v", err)
	}
	if got.Err == nil || got.Err.Error() != "ENOENT" {
		t.Errorf("error not round-tripped: %v", got.Err)
	}
}

func TestRecordRoundTrip_Xattr(t *testing.T) {
	rec := &connectors.Record{
		Pathname:  "/etc/hello.txt",
		IsXattr:   true,
		XattrName: "user.comment",
		XattrType: objects.AttributeExtended,
		Reader:    io.NopCloser(strings.NewReader("data")),
	}

	pb := RecordToProto(rec)
	if pb.FileInfo != nil {
		t.Errorf("xattr record should not include FileInfo")
	}
	if !pb.HasReader {
		t.Errorf("xattr record with a Reader should have HasReader=true")
	}

	got, err := RecordFromProto(pb)
	if err != nil {
		t.Fatalf("RecordFromProto: %v", err)
	}
	if !got.IsXattr || got.XattrName != "user.comment" {
		t.Errorf("xattr fields lost: %+v", got)
	}
}

func TestRecordFromProto_MissingFileInfoIsError(t *testing.T) {
	// Regular (non-xattr, no error) record arriving with no FileInfo
	// must fail rather than silently produce a zero FileInfo.
	bad := &Record{
		Pathname: "/somewhere",
	}
	if _, err := RecordFromProto(bad); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("expected ErrInvalidRecord, got %v", err)
	}
}

func TestRecordToProto_NoReader_DirectoryHasNoReader(t *testing.T) {
	rec := &connectors.Record{
		Pathname: "/etc",
		FileInfo: objects.FileInfo{
			Lname: "etc",
			Lmode: fs.ModeDir | 0o755,
		},
	}
	pb := RecordToProto(rec)
	if pb.HasReader {
		t.Errorf("directory record should not advertise a reader")
	}
}

func TestRecordToProto_RegularButNilReader(t *testing.T) {
	rec := &connectors.Record{
		Pathname: "/etc/hello.txt",
		FileInfo: sampleFileInfo(),
	}
	pb := RecordToProto(rec)
	if pb.HasReader {
		t.Errorf("nil reader should yield HasReader=false")
	}
}

func TestResultRoundTrip(t *testing.T) {
	r := &connectors.Result{
		Record: connectors.Record{
			Pathname: "/etc/hello.txt",
			FileInfo: sampleFileInfo(),
		},
		Err: errors.New("boom"),
	}
	pb := ResultToProto(r)
	if pb.Error != "boom" {
		t.Errorf("error lost: %q", pb.Error)
	}
	got, err := ResultFromProto(pb)
	if err != nil {
		t.Fatalf("ResultFromProto: %v", err)
	}
	if got.Err == nil || got.Err.Error() != "boom" {
		t.Errorf("result error lost: %v", got.Err)
	}
	if got.Record.Pathname != r.Record.Pathname {
		t.Errorf("record pathname lost")
	}
}
