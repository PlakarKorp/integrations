package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/PlakarKorp/integrations-private/mongodb/tests/testhelpers"
)

// TestPing checks `plakar source ping` and `plakar destination ping`, which
// call the connectors' Ping: OK against a reachable server, and mongosh's own
// error otherwise.
func TestPing(t *testing.T) {
	ctx := context.Background()

	certs := testhelpers.GenerateCerts(t, "mongo")
	net := testhelpers.NewNetwork(ctx, t)
	testhelpers.StartMongoContainer(ctx, t, net, "mongo", certs, true)
	plakar := testhelpers.StartPlakarContainer(ctx, t, net, certs.Files())

	cases := []struct {
		name  string
		opts  []string
		fails bool
		want  string
	}{
		{name: "ok", opts: []string{"tls_ca_cert=" + testhelpers.CACertPath}, want: "configuration OK"},
		// mongosh cannot read the CA and says so on stderr only.
		{name: "missing_ca", opts: []string{"tls_ca_cert=/nonexistent.pem"}, fails: true, want: "/nonexistent.pem"},
	}

	for _, kind := range []string{"source", "destination"} {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				name := kind + "_" + tc.name
				testhelpers.ExecOK(ctx, t, plakar, append(
					[]string{"plakar", kind, "add", name, "mongodb://mongo"}, tc.opts...)...)

				ping := []string{"plakar", kind, "ping", name}
				var out string
				if tc.fails {
					out = testhelpers.ExecFails(ctx, t, plakar, ping...)
				} else {
					out = testhelpers.ExecCapture(ctx, t, plakar, ping...)
				}
				if !strings.Contains(out, tc.want) {
					t.Fatalf("ping output lacks %q:\n%s", tc.want, out)
				}
			})
		}
	}
}
