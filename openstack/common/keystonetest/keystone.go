package keystonetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// AuthRequest is the subset of a Keystone v3 token request the tests check.
type AuthRequest struct {
	Auth struct {
		Identity struct {
			Methods  []string `json:"methods"`
			Password *struct {
				User struct {
					Name     string `json:"name"`
					Password string `json:"password"`
					Domain   struct {
						Name string `json:"name"`
					} `json:"domain"`
				} `json:"user"`
			} `json:"password"`
			ApplicationCredential *struct {
				ID     string `json:"id"`
				Secret string `json:"secret"`
			} `json:"application_credential"`
		} `json:"identity"`
		Scope map[string]any `json:"scope"`
	} `json:"auth"`
}

// Serve serves Keystone's token endpoint and returns the auth URL to
// configure, plus the last token request it received. It answers with status;
// on success the token is scoped to project p1 and its catalog offers a
// compute service in each of regions.
func Serve(t *testing.T, status int, regions ...string) (string, *AuthRequest) {
	t.Helper()
	var endpoints []string
	for _, region := range regions {
		endpoints = append(endpoints, fmt.Sprintf(
			`{"interface":"public","region":%q,"region_id":%q,"url":"http://compute.%s.example/v2.1"}`,
			region, region, region))
	}
	body := fmt.Sprintf(`{"token":{"expires_at":"2999-01-01T00:00:00.000000Z","project":{"id":"p1"},
		"catalog":[{"type":"compute","endpoints":[%s]}]}}`, strings.Join(endpoints, ","))

	got := new(AuthRequest)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(got), "decode token request")
		if status != http.StatusCreated {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("X-Subject-Token", "fake-token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v3/", got
}
