package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/PlakarKorp/integrations-private/mongodb/tests/testhelpers"
	"github.com/testcontainers/testcontainers-go"
)

const seedScript = `
db.getSiblingDB("shop").products.insertMany([
	{name: "keyboard", price: NumberDecimal("49.90"), tags: ["usb", "azerty"]},
	{name: "mouse", price: NumberDecimal("19.00"), stock: NumberLong(42)},
]);
db.getSiblingDB("shop").customers.insertMany([
	{name: "alice", since: ISODate("2024-03-01T10:00:00Z"), address: {city: "Paris"}},
	{name: "bob", since: ISODate("2025-07-14T08:30:00Z"), vip: true},
]);
db.getSiblingDB("blog").posts.insertOne(
	{title: "No backups?", body: BinData(0, "cGxha2Fy")}
);
`

// createX509User lets the client certificate authenticate with MONGODB-X509
// and the given built-in role.
func createX509User(ctx context.Context, t *testing.T, container testcontainers.Container, subject, role string) {
	t.Helper()
	testhelpers.Mongosh(ctx, t, container, fmt.Sprintf(
		`db.getSiblingDB("$external").createUser({user: %q, roles: [{role: %q, db: "admin"}]})`,
		subject, role))
}

// TestRoundTrip backs a seeded server up through plakar, restores the
// snapshot to a fresh server, and checks both hold the same data, once per
// way of connecting and authenticating. Servers reached over TLS refuse
// plaintext, so a client ignoring its TLS options fails.
func TestRoundTrip(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		tls  bool
		opts []string
	}{
		{
			name: "plain",
			opts: []string{"use_tls=false",
				"username=" + testhelpers.RootUser, "password=" + testhelpers.RootPassword},
		},
		{
			name: "tls",
			tls:  true,
			opts: []string{"tls_ca_cert=" + testhelpers.CACertPath, "auth_mechanism=SCRAM-SHA-256",
				"username=" + testhelpers.RootUser, "password=" + testhelpers.RootPassword},
		},
		{
			name: "mtls",
			tls:  true,
			opts: []string{"tls_ca_cert=" + testhelpers.CACertPath, "tls_client_cert=" + testhelpers.ClientPEMPath,
				"auth_mechanism=SCRAM-SHA-256",
				"username=" + testhelpers.RootUser, "password=" + testhelpers.RootPassword},
		},
		{
			name: "x509",
			tls:  true,
			opts: []string{"tls_ca_cert=" + testhelpers.CACertPath, "tls_client_cert=" + testhelpers.ClientPEMPath,
				"auth_mechanism=MONGODB-X509"},
		},
	}

	hosts := []string{"mongo-src-plain", "mongo-src-tls"}
	for _, tc := range cases {
		hosts = append(hosts, "mongo-dst-"+tc.name)
	}
	certs := testhelpers.GenerateCerts(t, hosts...)
	net := testhelpers.NewNetwork(ctx, t)

	// One source per TLS mode, keyed by requireTLS, each with the dump a
	// restore must reproduce: _id values differ between the two.
	type source struct{ host, want string }
	sources := map[bool]source{}
	for requireTLS, host := range map[bool]string{false: "mongo-src-plain", true: "mongo-src-tls"} {
		src := testhelpers.StartMongoContainer(ctx, t, net, host, certs, requireTLS)
		testhelpers.Mongosh(ctx, t, src, seedScript)
		createX509User(ctx, t, src, certs.ClientSubject, "backup")
		sources[requireTLS] = source{host, testhelpers.Dump(ctx, t, src)}
	}

	plakar := testhelpers.StartPlakarContainer(ctx, t, net, certs.Files())

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dstHost := "mongo-dst-" + tc.name
			src := sources[tc.tls]
			dst := testhelpers.StartMongoContainer(ctx, t, net, dstHost, certs, tc.tls)
			createX509User(ctx, t, dst, certs.ClientSubject, "restore")

			var opts []string
			for _, o := range tc.opts {
				opts = append(opts, "-o", o)
			}

			store := "/var/backups/" + tc.name
			testhelpers.ExecOK(ctx, t, plakar, "plakar", "at", store, "create", "-plaintext")
			testhelpers.ExecOK(ctx, t, plakar, append(append(
				[]string{"plakar", "at", store, "backup"}, opts...), "mongodb://"+src.host)...)

			snapshots := testhelpers.ListSnapshots(ctx, t, plakar, store)
			if len(snapshots) != 1 {
				t.Fatalf("got %d snapshots, want 1", len(snapshots))
			}
			files := testhelpers.LsSnapshot(ctx, t, plakar, store, snapshots[0])
			if !strings.Contains(files, "mongodb-backup.bson") {
				t.Fatalf("snapshot has no mongodb-backup.bson:\n%s", files)
			}

			testhelpers.ExecOK(ctx, t, plakar, append(append(
				[]string{"plakar", "at", store, "restore", "-to", "mongodb://" + dstHost}, opts...), snapshots[0])...)

			if got := testhelpers.Dump(ctx, t, dst); got != src.want {
				t.Errorf("restored data differs from source\ngot:  %s\nwant: %s", got, src.want)
			}
		})
	}
}
