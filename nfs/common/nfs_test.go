package common

import (
	"os"
	"testing"

	"github.com/vmware/go-nfs-client/nfs"
)

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name       string
		config     map[string]string
		wantErr    bool
		wantHost   string
		wantPort   string
		wantExport string
		wantRoot   string
		wantUID    uint32
		wantGID    uint32
		wantOrigin string
	}{
		{
			name:       "basic export",
			config:     map[string]string{"location": "nfs://nas.example.com/exports/data"},
			wantHost:   "nas.example.com",
			wantExport: "/exports/data",
			wantRoot:   "/",
			wantOrigin: "nas.example.com",
		},
		{
			name:       "export with subpath and port in url",
			config:     map[string]string{"location": "nfs://10.0.0.5:2049/srv/share/sub/dir"},
			wantHost:   "10.0.0.5",
			wantPort:   "2049",
			wantExport: "/srv/share/sub/dir",
			wantRoot:   "/",
			wantOrigin: "10.0.0.5:2049",
		},
		{
			name: "root narrows walk, uid/gid set",
			config: map[string]string{
				"location": "nfs://host/export",
				"root":     "home/alice",
				"uid":      "1000",
				"gid":      "1000",
			},
			wantHost:   "host",
			wantExport: "/export",
			wantRoot:   "/home/alice",
			wantUID:    1000,
			wantGID:    1000,
			wantOrigin: "host",
		},
		{
			name:     "port param when absent from url",
			config:   map[string]string{"location": "nfs://host/export", "port": "20490"},
			wantPort: "20490",
		},
		{name: "missing location", config: map[string]string{}, wantErr: true},
		{name: "wrong scheme", config: map[string]string{"location": "sftp://host/export"}, wantErr: true},
		{name: "missing export", config: map[string]string{"location": "nfs://host"}, wantErr: true},
		{name: "missing export trailing slash", config: map[string]string{"location": "nfs://host/"}, wantErr: true},
		{name: "bad uid", config: map[string]string{"location": "nfs://host/e", "uid": "abc"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseConfig(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantHost != "" && cfg.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", cfg.Host, tt.wantHost)
			}
			if tt.wantPort != "" && cfg.Port != tt.wantPort {
				t.Errorf("Port = %q, want %q", cfg.Port, tt.wantPort)
			}
			if tt.wantExport != "" && cfg.Export != tt.wantExport {
				t.Errorf("Export = %q, want %q", cfg.Export, tt.wantExport)
			}
			if tt.wantRoot != "" && cfg.Root != tt.wantRoot {
				t.Errorf("Root = %q, want %q", cfg.Root, tt.wantRoot)
			}
			if cfg.UID != tt.wantUID {
				t.Errorf("UID = %d, want %d", cfg.UID, tt.wantUID)
			}
			if cfg.GID != tt.wantGID {
				t.Errorf("GID = %d, want %d", cfg.GID, tt.wantGID)
			}
			if tt.wantOrigin != "" && cfg.Origin() != tt.wantOrigin {
				t.Errorf("Origin() = %q, want %q", cfg.Origin(), tt.wantOrigin)
			}
		})
	}
}

func TestFattrMode(t *testing.T) {
	tests := []struct {
		name string
		attr *nfs.Fattr
		want os.FileMode
	}{
		{
			name: "regular file 0644",
			attr: &nfs.Fattr{Type: nf3Reg, FileMode: 0o644},
			want: 0o644,
		},
		{
			name: "directory 0755",
			attr: &nfs.Fattr{Type: nf3Dir, FileMode: 0o755},
			want: os.ModeDir | 0o755,
		},
		{
			name: "symlink 0777",
			attr: &nfs.Fattr{Type: nf3Lnk, FileMode: 0o777},
			want: os.ModeSymlink | 0o777,
		},
		{
			name: "setuid root binary",
			attr: &nfs.Fattr{Type: nf3Reg, FileMode: 0o4755},
			want: os.ModeSetuid | 0o755,
		},
		{
			name: "sticky world-writable dir",
			attr: &nfs.Fattr{Type: nf3Dir, FileMode: 0o1777},
			want: os.ModeDir | os.ModeSticky | 0o777,
		},
		{
			name: "char device",
			attr: &nfs.Fattr{Type: nf3Chr, FileMode: 0o600},
			want: os.ModeDevice | os.ModeCharDevice | 0o600,
		},
		{
			name: "fifo",
			attr: &nfs.Fattr{Type: nf3FIFO, FileMode: 0o644},
			want: os.ModeNamedPipe | 0o644,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fattrMode(tt.attr); got != tt.want {
				t.Errorf("fattrMode() = %v (%o), want %v (%o)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestFileInfoFromAttr(t *testing.T) {
	attr := &nfs.Fattr{
		Type:     nf3Reg,
		FileMode: 0o640,
		Nlink:    2,
		UID:      1000,
		GID:      2000,
		Filesize: 4096,
		FSID:     42,
		Fileid:   99,
		Mtime:    nfs.NFS3Time{Seconds: 1_700_000_000},
	}

	fi := FileInfoFromAttr("file.txt", attr)
	if fi.Name() != "file.txt" {
		t.Errorf("Name = %q, want file.txt", fi.Name())
	}
	if fi.Size() != 4096 {
		t.Errorf("Size = %d, want 4096", fi.Size())
	}
	if fi.Mode() != 0o640 {
		t.Errorf("Mode = %o, want 640", fi.Mode())
	}
	if fi.Uid() != 1000 || fi.Gid() != 2000 {
		t.Errorf("uid/gid = %d/%d, want 1000/2000", fi.Uid(), fi.Gid())
	}
	if fi.Nlink() != 2 {
		t.Errorf("Nlink = %d, want 2", fi.Nlink())
	}
	if fi.Ino() != 99 || fi.Dev() != 42 {
		t.Errorf("ino/dev = %d/%d, want 99/42", fi.Ino(), fi.Dev())
	}
	if fi.ModTime().Unix() != 1_700_000_000 {
		t.Errorf("ModTime = %d, want 1700000000", fi.ModTime().Unix())
	}
}
