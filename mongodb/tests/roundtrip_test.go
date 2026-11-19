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

// Credentials of a user created in the shop database, with read access to
// it only.
const (
	shopUser     = "shop_reader"
	shopPassword = "secret"
)

// options turns key=value pairs into plakar -o flags.
func options(kv []string) []string {
	var opts []string
	for _, o := range kv {
		opts = append(opts, "-o", o)
	}
	return opts
}

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

	root := []string{"username=" + testhelpers.RootUser, "password=" + testhelpers.RootPassword}
	plain := append([]string{"use_tls=false"}, root...)
	tls := append([]string{"tls_ca_cert=" + testhelpers.CACertPath, "auth_mechanism=SCRAM-SHA-256"}, root...)
	mtls := append([]string{"tls_client_cert=" + testhelpers.ClientPEMPath}, tls...)
	x509 := []string{"tls_ca_cert=" + testhelpers.CACertPath, "tls_client_cert=" + testhelpers.ClientPEMPath,
		"auth_mechanism=MONGODB-X509"}

	cases := []struct {
		name    string
		tls     bool
		backup  []string
		restore []string
		// dbs lists the databases expected on the target, all when empty.
		dbs []string
	}{
		{name: "plain", backup: plain, restore: plain},
		{name: "tls", tls: true, backup: tls, restore: tls},
		{name: "mtls", tls: true, backup: mtls, restore: mtls},
		{name: "x509", tls: true, backup: x509, restore: x509},
		{
			name: "database",
			backup: []string{"use_tls=false", "database=shop",
				"username=" + shopUser, "password=" + shopPassword},
			restore: plain,
			dbs:     []string{"shop"},
		},
	}

	hosts := []string{"mongo-src-plain", "mongo-src-tls"}
	for _, tc := range cases {
		hosts = append(hosts, "mongo-dst-"+tc.name)
	}
	certs := testhelpers.GenerateCerts(t, hosts...)
	net := testhelpers.NewNetwork(ctx, t)

	// One source per TLS mode, keyed by requireTLS.
	type source struct {
		host      string
		container testcontainers.Container
	}
	sources := map[bool]source{}
	for requireTLS, host := range map[bool]string{false: "mongo-src-plain", true: "mongo-src-tls"} {
		src := testhelpers.StartMongoContainer(ctx, t, net, host, certs, requireTLS)
		testhelpers.Mongosh(ctx, t, src, seedScript)
		createX509User(ctx, t, src, certs.ClientSubject, "backup")
		testhelpers.Mongosh(ctx, t, src, fmt.Sprintf(
			`db.getSiblingDB("shop").createUser({user: %q, pwd: %q, roles: [{role: "read", db: "shop"}]})`,
			shopUser, shopPassword))
		sources[requireTLS] = source{host, src}
	}

	plakar := testhelpers.StartPlakarContainer(ctx, t, net, certs.Files())

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dstHost := "mongo-dst-" + tc.name
			src := sources[tc.tls]
			dst := testhelpers.StartMongoContainer(ctx, t, net, dstHost, certs, tc.tls)
			createX509User(ctx, t, dst, certs.ClientSubject, "restore")

			store := "/var/backups/" + tc.name
			testhelpers.ExecOK(ctx, t, plakar, "plakar", "at", store, "create", "-plaintext")
			testhelpers.ExecOK(ctx, t, plakar, append(append(
				[]string{"plakar", "at", store, "backup"}, options(tc.backup)...), "mongodb://"+src.host)...)

			snapshots := testhelpers.ListSnapshots(ctx, t, plakar, store)
			if len(snapshots) != 1 {
				t.Fatalf("got %d snapshots, want 1", len(snapshots))
			}
			files := testhelpers.LsSnapshot(ctx, t, plakar, store, snapshots[0])
			if !strings.Contains(files, "mongodb-backup.bson") {
				t.Fatalf("snapshot has no mongodb-backup.bson:\n%s", files)
			}

			testhelpers.ExecOK(ctx, t, plakar, append(append(
				[]string{"plakar", "at", store, "restore", "-to", "mongodb://" + dstHost}, options(tc.restore)...), snapshots[0])...)

			want := testhelpers.Dump(ctx, t, src.container, tc.dbs...)
			if got := testhelpers.Dump(ctx, t, dst); got != want {
				t.Errorf("restored data differs from source\ngot:  %s\nwant: %s", got, want)
			}
		})
	}
}
