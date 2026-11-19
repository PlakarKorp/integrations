package xapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PlakarKorp/integration-xcp-ng/xcp-ng/xapi"
	"github.com/stretchr/testify/require"
)

type rpcRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
	ID      json.RawMessage   `json:"id"`
	Host    string            `json:"-"`
}

func (r rpcRequest) param(t *testing.T, i int) string {
	t.Helper()
	var s string
	require.NoError(t, json.Unmarshal(r.Params[i], &s))
	return s
}

// handler answers one JSON-RPC call: a result, or a XAPI error description
// ["CODE", "param", ...].
type handler func(req rpcRequest) (result any, failure []string)

// server is a fake XAPI that records every request it receives.
type server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []rpcRequest
}

func newServer(t *testing.T, handle handler) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/jsonrpc" {
			http.NotFound(w, r)
			return
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Host = r.Host
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()

		result, failure := handle(req)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if failure != nil {
			resp["error"] = map[string]any{"code": 1, "message": failure[0], "data": failure}
		} else {
			resp["result"] = result
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) calls(method string) []rpcRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []rpcRequest
	for _, r := range s.requests {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// loginThen serves logins with successive session refs and hands the rest to
// handle.
func loginThen(sessions []string, handle handler) handler {
	var logins int
	return func(req rpcRequest) (any, []string) {
		if req.Method == "session.login_with_password" {
			session := sessions[min(logins, len(sessions)-1)]
			logins++
			return session, nil
		}
		return handle(req)
	}
}

func newClient(t *testing.T, srv *server) *xapi.Client {
	t.Helper()
	c, err := xapi.New(xapi.Config{URL: srv.URL, InsecureSkipVerify: true})
	require.NoError(t, err)
	return c
}

func loggedIn(t *testing.T, srv *server) *xapi.Client {
	t.Helper()
	c := newClient(t, srv)
	require.NoError(t, c.Login(context.Background(), "root", "hunter2"))
	return c
}

func TestLoginSendsCredentialsAndOriginator(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1"}, nil))

	c := loggedIn(t, srv)

	require.Equal(t, "OpaqueRef:s1", c.Session())
	logins := srv.calls("session.login_with_password")
	require.Len(t, logins, 1)
	require.Equal(t, "2.0", logins[0].JSONRPC)
	require.Len(t, logins[0].Params, 4)
	require.Equal(t, "root", logins[0].param(t, 0))
	require.Equal(t, "hunter2", logins[0].param(t, 1))
	require.Equal(t, xapi.Originator, logins[0].param(t, 3))
}

func TestLoginFailureIsTyped(t *testing.T) {
	srv := newServer(t, func(rpcRequest) (any, []string) {
		return nil, []string{xapi.CodeSessionAuthenticationFailed, "root", "Authentication failure"}
	})

	err := newClient(t, srv).Login(context.Background(), "root", "wrong")

	require.True(t, xapi.IsCode(err, xapi.CodeSessionAuthenticationFailed), err)
	require.Empty(t, newClient(t, srv).Session())
	require.NotContains(t, err.Error(), "wrong")
}

func TestCallPrependsSessionAndDecodesResult(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1"}, func(req rpcRequest) (any, []string) {
		return map[string]any{"uuid": "vm-1", "name_label": "web"}, nil
	}))
	c := loggedIn(t, srv)

	var record struct {
		UUID      string `json:"uuid"`
		NameLabel string `json:"name_label"`
	}
	require.NoError(t, c.Call(context.Background(), "VM.get_record", &record, "OpaqueRef:vm1"))

	require.Equal(t, "vm-1", record.UUID)
	require.Equal(t, "web", record.NameLabel)
	calls := srv.calls("VM.get_record")
	require.Len(t, calls, 1)
	require.Len(t, calls[0].Params, 2)
	require.Equal(t, "OpaqueRef:s1", calls[0].param(t, 0))
	require.Equal(t, "OpaqueRef:vm1", calls[0].param(t, 1))
}

func TestCallWithNilOut(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1"}, func(rpcRequest) (any, []string) {
		return "ignored", nil
	}))

	require.NoError(t, loggedIn(t, srv).Call(context.Background(), "VM.set_name_label", nil, "OpaqueRef:vm1", "x"))
}

func TestCallRequiresLogin(t *testing.T) {
	srv := newServer(t, func(rpcRequest) (any, []string) { return nil, nil })

	err := newClient(t, srv).Call(context.Background(), "VM.get_record", nil, "OpaqueRef:vm1")

	require.ErrorContains(t, err, "not logged in")
	require.Empty(t, srv.calls("VM.get_record"))
}

func TestReturnsTypedError(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1"}, func(rpcRequest) (any, []string) {
		return nil, []string{xapi.CodeOperationNotAllowed, "snapshot in progress"}
	}))

	err := loggedIn(t, srv).Call(context.Background(), "VM.snapshot", nil, "OpaqueRef:vm1", "snap")

	xapiErr, ok := errors.AsType[*xapi.Error](err)
	require.True(t, ok, err)
	require.Equal(t, "VM.snapshot", xapiErr.Method)
	require.Equal(t, xapi.CodeOperationNotAllowed, xapiErr.Code)
	require.Equal(t, []string{"snapshot in progress"}, xapiErr.Params)
	require.True(t, xapi.IsCode(err, xapi.CodeOperationNotAllowed))
	require.False(t, xapi.IsCode(err, xapi.CodeHandleInvalid))
	require.Equal(t, "xapi: VM.snapshot: OPERATION_NOT_ALLOWED (snapshot in progress)", err.Error())
}

func TestErrorWithoutDescriptionUsesMessage(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32601,"message":"Method not found"},"id":1}`))
	}))
	defer srv.Close()
	c, err := xapi.New(xapi.Config{URL: srv.URL, InsecureSkipVerify: true})
	require.NoError(t, err)

	err = c.Login(context.Background(), "root", "hunter2")

	require.True(t, xapi.IsCode(err, "Method not found"), err)
}

func TestReloginOnSessionInvalid(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1", "OpaqueRef:s2"}, func(req rpcRequest) (any, []string) {
		if req.param(t, 0) == "OpaqueRef:s1" {
			return nil, []string{xapi.CodeSessionInvalid, "OpaqueRef:s1"}
		}
		return "ok", nil
	}))
	c := loggedIn(t, srv)

	var out string
	require.NoError(t, c.Call(context.Background(), "VM.get_record", &out, "OpaqueRef:vm1"))

	require.Equal(t, "ok", out)
	require.Equal(t, "OpaqueRef:s2", c.Session())
	require.Len(t, srv.calls("session.login_with_password"), 2)
	calls := srv.calls("VM.get_record")
	require.Len(t, calls, 2)
	require.Equal(t, "OpaqueRef:s2", calls[1].param(t, 0))
}

func TestReloginOnlyOnce(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1"}, func(req rpcRequest) (any, []string) {
		return nil, []string{xapi.CodeSessionInvalid, req.param(t, 0)}
	}))
	c := loggedIn(t, srv)

	err := c.Call(context.Background(), "VM.get_record", nil, "OpaqueRef:vm1")

	require.True(t, xapi.IsCode(err, xapi.CodeSessionInvalid), err)
	require.Len(t, srv.calls("session.login_with_password"), 2)
	require.Len(t, srv.calls("VM.get_record"), 2)
}

func TestLoginFollowsHostIsSlave(t *testing.T) {
	// The server is reached as 127.0.0.1 first; the "master" it points at is
	// the same server under its localhost name.
	srv := newServer(t, func(req rpcRequest) (any, []string) {
		if !strings.HasPrefix(req.Host, "localhost:") {
			return nil, []string{xapi.CodeHostIsSlave, "localhost"}
		}
		return "OpaqueRef:master", nil
	})

	c := loggedIn(t, srv)

	require.Equal(t, "OpaqueRef:master", c.Session())
	logins := srv.calls("session.login_with_password")
	require.Len(t, logins, 2)
	require.True(t, strings.HasPrefix(logins[0].Host, "127.0.0.1:"), logins[0].Host)
	require.True(t, strings.HasPrefix(logins[1].Host, "localhost:"), logins[1].Host)
}

func TestCallFollowsHostIsSlave(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1", "OpaqueRef:s2"}, func(req rpcRequest) (any, []string) {
		if !strings.HasPrefix(req.Host, "localhost:") {
			return nil, []string{xapi.CodeHostIsSlave, "localhost"}
		}
		return "ok", nil
	}))
	c := loggedIn(t, srv)

	var out string
	require.NoError(t, c.Call(context.Background(), "VM.get_record", &out, "OpaqueRef:vm1"))

	require.Equal(t, "ok", out)
	calls := srv.calls("VM.get_record")
	require.Len(t, calls, 2)
	require.True(t, strings.HasPrefix(calls[1].Host, "localhost:"), calls[1].Host)
	require.Equal(t, "OpaqueRef:s2", calls[1].param(t, 0))
}

func TestLogoutClearsSession(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1"}, func(rpcRequest) (any, []string) {
		return "", nil
	}))
	c := loggedIn(t, srv)

	require.NoError(t, c.Logout(context.Background()))

	require.Empty(t, c.Session())
	logouts := srv.calls("session.logout")
	require.Len(t, logouts, 1)
	require.Equal(t, "OpaqueRef:s1", logouts[0].param(t, 0))

	// A second logout is a no-op.
	require.NoError(t, c.Logout(context.Background()))
	require.Len(t, srv.calls("session.logout"), 1)
}

func TestLogoutToleratesDroppedSession(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1"}, func(rpcRequest) (any, []string) {
		return nil, []string{xapi.CodeSessionInvalid, "OpaqueRef:s1"}
	}))
	c := loggedIn(t, srv)

	require.NoError(t, c.Logout(context.Background()))
	require.Empty(t, c.Session())
}

func TestNonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, err := xapi.New(xapi.Config{URL: srv.URL, InsecureSkipVerify: true})
	require.NoError(t, err)

	err = c.Login(context.Background(), "root", "hunter2")

	require.ErrorContains(t, err, "HTTP 500")
	_, ok := errors.AsType[*xapi.Error](err)
	require.False(t, ok)
}

func TestCapsResponseSize(t *testing.T) {
	srv := newServer(t, func(rpcRequest) (any, []string) {
		return strings.Repeat("a", 2048), nil
	})
	c, err := xapi.New(xapi.Config{URL: srv.URL, InsecureSkipVerify: true, MaxResponseSize: 1024})
	require.NoError(t, err)

	err = c.Login(context.Background(), "root", "hunter2")

	_, ok := errors.AsType[*http.MaxBytesError](err)
	require.True(t, ok, err)
}

func TestVerifiesTLSByDefault(t *testing.T) {
	srv := newServer(t, loginThen([]string{"OpaqueRef:s1"}, nil))
	c, err := xapi.New(xapi.Config{URL: srv.URL})
	require.NoError(t, err)

	err = c.Login(context.Background(), "root", "hunter2")

	require.Error(t, err)
	require.Empty(t, srv.calls("session.login_with_password"))
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	for name, cfg := range map[string]xapi.Config{
		"empty URL":    {},
		"no scheme":    {URL: "xcp.example.com"},
		"wrong scheme": {URL: "ftp://xcp.example.com"},
		"http":         {URL: "http://xcp.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := xapi.New(cfg)
			require.Error(t, err)
		})
	}
}

func TestNewDoesNotLeakURLPassword(t *testing.T) {
	for name, u := range map[string]string{
		"unparsable URL": "https://user:hunter2@xcp.example.com/%zz",
		"wrong scheme":   "ftp://user:hunter2@xcp.example.com",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := xapi.New(xapi.Config{URL: u})
			require.Error(t, err)
			require.NotContains(t, err.Error(), "hunter2")
		})
	}
}

func TestInt64AcceptsNumberAndString(t *testing.T) {
	var out struct {
		Number xapi.Int64 `json:"n"`
		String xapi.Int64 `json:"s"`
		Null   xapi.Int64 `json:"z"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"n": 4294967296, "s": "8589934592", "z": null}`), &out))
	require.Equal(t, xapi.Int64(4294967296), out.Number)
	require.Equal(t, xapi.Int64(8589934592), out.String)
	require.Equal(t, xapi.Int64(0), out.Null)

	require.Error(t, json.Unmarshal([]byte(`{"n": "lots"}`), &out))
}