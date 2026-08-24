package openwrtclient

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	loginOK      = `{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"cafe"}]}`
	loginDenied  = `{"jsonrpc":"2.0","id":1,"result":[6]}`
	loginRPCFail = `{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Access denied"}}`
)

func execReply(code int, stderr string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":[0,{"code":%d,"stdout":"","stderr":%q}]}`,
		code, stderr)
}

// newClient wires a client onto a test server.
func newClient(t *testing.T, srv *httptest.Server) *OpenWRTClient {
	t.Helper()

	clt := NewBackupClient(srv.URL, "root", "hunter2", WithHTTPClient(srv.Client()))
	return &clt
}

func serve(t *testing.T, mux *http.ServeMux) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGetSession(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "success", status: 200, body: loginOK},
		{name: "bad credentials", status: 200, body: loginDenied, wantErr: "invalid credentials"},
		{name: "rpc error", status: 200, body: loginRPCFail, wantErr: "Access denied"},
		{name: "http error", status: 403, body: "", wantErr: "cannot get session"},
		{name: "garbage body", status: 200, body: "not json", wantErr: "cannot decode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})

			clt := newClient(t, serve(t, mux))
			err := clt.getSession(context.Background())

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("getSession: %v", err)
				}
				if clt.session != "cafe" {
					t.Errorf("session = %q, want cafe", clt.session)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestGetBackupArchive(t *testing.T) {
	const payload = "not really a tarball, but bytes all the same"

	t.Run("success", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, loginOK)
		})
		mux.HandleFunc("/cgi-bin/cgi-backup", func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if got := r.PostForm.Get("sessionid"); got != "cafe" {
				t.Errorf("sessionid = %q, want cafe", got)
			}
			w.Header().Set("Content-Disposition", `attachment; filename="backup-openwrt.tar.gz"`)
			_, _ = io.WriteString(w, payload)
		})

		clt := newClient(t, serve(t, mux))
		bf, err := clt.GetBackupArchive(context.Background())
		if err != nil {
			t.Fatalf("GetBackupArchive: %v", err)
		}
		defer func() { _ = bf.Close() }()

		if bf.Filename != "backup-openwrt.tar.gz" {
			t.Errorf("Filename = %q", bf.Filename)
		}
		got, err := io.ReadAll(bf)
		if err != nil {
			t.Fatalf("reading archive: %v", err)
		}
		if string(got) != payload {
			t.Errorf("archive = %q, want %q", got, payload)
		}
	})

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "http error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "forbidden", http.StatusForbidden)
			},
			wantErr: "cannot fetch archive",
		},
		{
			name: "no content disposition",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, payload)
			},
			wantErr: "cannot parse Content-Disposition",
		},
		{
			name: "no filename",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Disposition", "attachment")
				_, _ = io.WriteString(w, payload)
			},
			wantErr: "no filename",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, loginOK)
			})
			mux.HandleFunc("/cgi-bin/cgi-backup", tc.handler)

			clt := newClient(t, serve(t, mux))
			if _, err := clt.GetBackupArchive(context.Background()); err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// uploadHandler mimics cgi-io: it reads the parts back and answers with what
// it claims to have written. The tweak hook lets a test lie about it.
func uploadHandler(t *testing.T, tweak func(data []byte, res *uploadResult)) (http.HandlerFunc, *[]string, *[]byte) {
	t.Helper()

	order := []string{}
	var received []byte

	return func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("Content-Type: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, err := io.ReadAll(p)
			if err != nil {
				t.Errorf("reading part %s: %v", p.FormName(), err)
				return
			}
			order = append(order, p.FormName())
			switch p.FormName() {
			case "filedata":
				received = b
				// cgi-io needs a plain form field here, not a file part.
				if fn := p.FileName(); fn != "" {
					t.Errorf("filedata must not carry a filename, got %q", fn)
				}
			case "filename":
				if string(b) != "/tmp/backup.tar.gz" {
					t.Errorf("filename = %q", b)
				}
			case "sessionid":
				if string(b) != "cafe" {
					t.Errorf("sessionid = %q", b)
				}
			}
		}

		sum := md5.Sum(received)
		sha := sha256.Sum256(received)
		res := uploadResult{
			Size:      int64(len(received)),
			Checksum:  hex.EncodeToString(sum[:]),
			SHA256Sum: hex.EncodeToString(sha[:]),
		}
		if tweak != nil {
			tweak(received, &res)
		}
		_ = json.NewEncoder(w).Encode(res)
	}, &order, &received
}

func TestUploadArchive(t *testing.T) {
	archive := bytes.NewBufferString("gzip bytes \x00\xff\x1b and more")

	for _, tc := range []struct {
		name    string
		tweak   func(data []byte, res *uploadResult)
		wantErr string
	}{
		{name: "md5 and sha256 match"},
		{
			name: "sha256 absent, md5 checked",
			tweak: func(_ []byte, res *uploadResult) {
				res.SHA256Sum = ""
			},
		},
		{
			name: "device wrote fewer bytes",
			tweak: func(_ []byte, res *uploadResult) {
				res.Size -= 10
			},
			wantErr: "size mismatch",
		},
		{
			name: "sha256 mismatch",
			tweak: func(_ []byte, res *uploadResult) {
				res.SHA256Sum = strings.Repeat("0", 64)
			},
			wantErr: "sha256sum mismatch",
		},
		{
			name: "md5 mismatch when sha256 is absent",
			tweak: func(_ []byte, res *uploadResult) {
				res.SHA256Sum = ""
				res.Checksum = strings.Repeat("0", 32)
			},
			wantErr: "checksum mismatch",
		},
		{
			name: "device returns no checksum at all",
			tweak: func(_ []byte, res *uploadResult) {
				res.SHA256Sum, res.Checksum = "", ""
			},
			wantErr: "no checksum",
		},
		{
			// cgi-io reports uppercase hex on some builds.
			name: "uppercase hex is accepted",
			tweak: func(_ []byte, res *uploadResult) {
				res.SHA256Sum = strings.ToUpper(res.SHA256Sum)
				res.Checksum = strings.ToUpper(res.Checksum)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, _, received := uploadHandler(t, tc.tweak)
			mux := http.NewServeMux()
			mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, loginOK)
			})
			mux.HandleFunc("/cgi-bin/cgi-upload", handler)

			clt := newClient(t, serve(t, mux))
			sent := bytes.NewBuffer(archive.Bytes())
			err := clt.UploadArchive(context.Background(), sent)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("UploadArchive: %v", err)
				}
				if !bytes.Equal(*received, archive.Bytes()) {
					t.Errorf("the device received %d bytes, sent %d", len(*received), archive.Len())
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestUploadArchiveFieldOrder locks in what cgi-io expects: sessionid and
// filename must precede filedata.
func TestUploadArchiveFieldOrder(t *testing.T) {
	handler, order, _ := uploadHandler(t, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, loginOK)
	})
	mux.HandleFunc("/cgi-bin/cgi-upload", handler)

	clt := newClient(t, serve(t, mux))
	if err := clt.UploadArchive(context.Background(), bytes.NewBufferString("data")); err != nil {
		t.Fatalf("UploadArchive: %v", err)
	}

	want := []string{"sessionid", "filename", "filedata"}
	if strings.Join(*order, ",") != strings.Join(want, ",") {
		t.Errorf("part order = %v, want %v", *order, want)
	}
}

func TestUploadArchiveHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, loginOK)
	})
	mux.HandleFunc("/cgi-bin/cgi-upload", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})

	clt := newClient(t, serve(t, mux))
	err := clt.UploadArchive(context.Background(), bytes.NewBufferString("data"))
	if err == nil || !strings.Contains(err.Error(), "error uploading archive") {
		t.Fatalf("error = %v, want an upload failure", err)
	}
}

func TestSysupgradeAndReboot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    func(*OpenWRTClient) error
		status  int
		body    string
		wantErr string
	}{
		{
			name:   "sysupgrade success",
			call:   func(c *OpenWRTClient) error { return c.Sysupgrade(context.Background()) },
			status: 200, body: execReply(0, ""),
		},
		{
			name:   "sysupgrade command failed",
			call:   func(c *OpenWRTClient) error { return c.Sysupgrade(context.Background()) },
			status: 200, body: execReply(1, "no space left"),
			wantErr: "no space left",
		},
		{
			name:   "sysupgrade http error",
			call:   func(c *OpenWRTClient) error { return c.Sysupgrade(context.Background()) },
			status: 500, body: "",
			wantErr: "cannot perform sysupgrade",
		},
		{
			name:   "reboot success",
			call:   func(c *OpenWRTClient) error { return c.RebootDevice(context.Background()) },
			status: 200, body: execReply(0, ""),
		},
		{
			name:   "reboot command failed",
			call:   func(c *OpenWRTClient) error { return c.RebootDevice(context.Background()) },
			status: 200, body: execReply(1, "cannot reboot"),
			wantErr: "cannot reboot",
		},
		{
			// This message used to say "sysupgrade", which sent whoever read
			// it looking at the wrong step.
			name:   "reboot http error names the reboot",
			call:   func(c *OpenWRTClient) error { return c.RebootDevice(context.Background()) },
			status: 500, body: "",
			wantErr: "cannot perform reboot",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			mux := http.NewServeMux()
			mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 { // the login
					_, _ = io.WriteString(w, loginOK)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})

			err := tc.call(newClient(t, serve(t, mux)))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestPing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, loginDenied)
	})

	clt := newClient(t, serve(t, mux))
	if err := clt.Ping(context.Background()); err == nil {
		t.Fatal("Ping should surface bad credentials")
	}
}

// TestSessionIsReused checks we do not renegotiate a session for every call.
func TestSessionIsReused(t *testing.T) {
	logins := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/ubus", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"login"`) {
			logins++
			_, _ = io.WriteString(w, loginOK)
			return
		}
		_, _ = io.WriteString(w, execReply(0, ""))
	})

	clt := newClient(t, serve(t, mux))
	ctx := context.Background()
	if err := clt.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := clt.Sysupgrade(ctx); err != nil {
		t.Fatalf("Sysupgrade: %v", err)
	}
	if err := clt.RebootDevice(ctx); err != nil {
		t.Fatalf("RebootDevice: %v", err)
	}

	if logins != 1 {
		t.Errorf("logged in %d times, want 1", logins)
	}
}

func TestNewBackupClientTrimsTrailingSlash(t *testing.T) {
	clt := NewBackupClient("https://192.168.1.1/", "root", "")
	if clt.baseURL != "https://192.168.1.1" {
		t.Errorf("baseURL = %q, want https://192.168.1.1", clt.baseURL)
	}
}
