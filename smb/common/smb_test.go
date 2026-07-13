package common

import "testing"

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]string
		wantErr   bool
		wantHost  string
		wantPort  string
		wantShare string
		wantRoot  string
		wantUser  string
		wantPass  string
		wantOrig  string
	}{
		{
			name:      "basic share",
			config:    map[string]string{"location": "smb://nas.example.com/documents"},
			wantHost:  "nas.example.com",
			wantPort:  "445",
			wantShare: "documents",
			wantRoot:  "/",
			wantUser:  "Guest",
			wantOrig:  "nas.example.com/documents",
		},
		{
			name:      "share with subpath and creds in url",
			config:    map[string]string{"location": "smb://alice:secret@10.0.0.9/data/projects/2025"},
			wantHost:  "10.0.0.9",
			wantShare: "data",
			wantRoot:  "/projects/2025",
			wantUser:  "alice",
			wantPass:  "secret",
			wantOrig:  "10.0.0.9/data",
		},
		{
			name: "root option overrides subpath, creds as options",
			config: map[string]string{
				"location": "smb://host/share/ignored",
				"root":     "wanted/dir",
				"username": "bob",
				"password": "pw",
				"domain":   "CORP",
			},
			wantShare: "share",
			wantRoot:  "/wanted/dir",
			wantUser:  "bob",
			wantPass:  "pw",
		},
		{
			name:     "explicit port in url",
			config:   map[string]string{"location": "smb://host:4445/share"},
			wantPort: "4445",
			wantOrig: "host:4445/share",
		},
		{
			name: "share param supplies share with bare location",
			config: map[string]string{
				"location": "smb://nas.example.com",
				"share":    "documents",
			},
			wantHost:  "nas.example.com",
			wantShare: "documents",
			wantRoot:  "/",
			wantUser:  "Guest",
			wantOrig:  "nas.example.com/documents",
		},
		{
			name: "share param takes precedence, url subpath still narrows walk",
			config: map[string]string{
				"location": "smb://host/ignored/projects/2025",
				"share":    "data",
			},
			wantHost:  "host",
			wantShare: "data",
			wantRoot:  "/projects/2025",
			wantUser:  "Guest",
		},
		{name: "missing location", config: map[string]string{}, wantErr: true},
		{name: "wrong scheme", config: map[string]string{"location": "nfs://host/share"}, wantErr: true},
		{name: "missing share", config: map[string]string{"location": "smb://host"}, wantErr: true},
		{name: "missing share trailing slash", config: map[string]string{"location": "smb://host/"}, wantErr: true},
		{
			name:    "user in url and option conflict",
			config:  map[string]string{"location": "smb://alice@host/share", "username": "bob"},
			wantErr: true,
		},
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
			if tt.wantShare != "" && cfg.Share != tt.wantShare {
				t.Errorf("Share = %q, want %q", cfg.Share, tt.wantShare)
			}
			if tt.wantRoot != "" && cfg.Root != tt.wantRoot {
				t.Errorf("Root = %q, want %q", cfg.Root, tt.wantRoot)
			}
			if tt.wantUser != "" && cfg.User != tt.wantUser {
				t.Errorf("User = %q, want %q", cfg.User, tt.wantUser)
			}
			if tt.wantPass != "" && cfg.Password != tt.wantPass {
				t.Errorf("Password = %q, want %q", cfg.Password, tt.wantPass)
			}
			if tt.wantOrig != "" && cfg.Origin() != tt.wantOrig {
				t.Errorf("Origin() = %q, want %q", cfg.Origin(), tt.wantOrig)
			}
		})
	}
}

func TestSplitShare(t *testing.T) {
	tests := []struct {
		in        string
		wantShare string
		wantSub   string
	}{
		{"/documents", "documents", "/"},
		{"/documents/", "documents", "/"},
		{"/data/projects/2025", "data", "/projects/2025"},
		{"/", "", "/"},
		{"", "", "/"},
		{"/share/a/b/../c", "share", "/a/c"},
	}
	for _, tt := range tests {
		share, sub := splitShare(tt.in)
		if share != tt.wantShare || sub != tt.wantSub {
			t.Errorf("splitShare(%q) = (%q,%q), want (%q,%q)", tt.in, share, sub, tt.wantShare, tt.wantSub)
		}
	}
}

func TestSharePath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/", "."},
		{"", "."},
		{"/foo.txt", "foo.txt"},
		{"/a/b/c.txt", "a/b/c.txt"},
	}
	for _, tt := range tests {
		if got := SharePath(tt.in); got != tt.want {
			t.Errorf("SharePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
