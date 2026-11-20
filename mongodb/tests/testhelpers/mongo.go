package testhelpers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MongoImage is the MongoDB server image. The plakar test image takes its
// MongoDB tools from it too.
const MongoImage = "mongodb/mongodb-community-server:8.0-ubuntu2204@sha256:fe5f7a181953d5e60fd4f35ff221700a121703df48f01221be1316ebe949b365"

// Credentials of the root user created in the admin database.
const (
	RootUser     = "root"
	RootPassword = "secret"
)

// StartMongoContainer starts a MongoDB server attached to net under alias.
// With requireTLS it refuses plaintext connections, otherwise it accepts
// both. TLS clients may or may not present a certificate. The container is
// terminated when the test ends.
func StartMongoContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork, alias string, certs *Certs, requireTLS bool) testcontainers.Container {
	t.Helper()

	tlsMode := "allowTLS"
	if requireTLS {
		tlsMode = "requireTLS"
	}
	req := testcontainers.ContainerRequest{
		Image: MongoImage,
		Cmd: []string{
			"--tlsMode", tlsMode,
			"--tlsCertificateKeyFile", ServerPEMPath,
			"--tlsCAFile", CACertPath,
			"--tlsAllowConnectionsWithoutCertificates",
		},
		Env: map[string]string{
			"MONGODB_INITDB_ROOT_USERNAME": RootUser,
			"MONGODB_INITDB_ROOT_PASSWORD": RootPassword,
		},
		Files:          certs.Files(),
		Networks:       []string{net.Name},
		NetworkAliases: map[string][]string{net.Name: {alias}},
		// The entrypoint creates the root user on a temporary mongod first,
		// so the second occurrence is the final server.
		WaitingFor: wait.ForLog(`"Waiting for connections"`).
			WithOccurrence(2).
			WithStartupTimeout(2 * time.Minute),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start mongo container %s: %v", alias, err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	return container
}

// Mongosh runs script as root inside a MongoDB container and returns what it
// prints.
func Mongosh(ctx context.Context, t *testing.T, container testcontainers.Container, script string) string {
	t.Helper()
	return ExecCapture(ctx, t, container, "mongosh", "--quiet", "--host", "localhost",
		"--tls", "--tlsCAFile", CACertPath,
		"-u", RootUser, "-p", RootPassword, "--authenticationDatabase", "admin",
		"--eval", script)
}

// dumpScript prints every document of every user database as canonical
// Extended JSON, collections and documents in a stable order. DBS restricts
// the dump to the databases it lists, when not empty.
const dumpScript = `
const only = DBS;
const out = {};
const names = db.adminCommand({listDatabases: 1}).databases.map(d => d.name)
	.filter(n => !["admin", "config", "local"].includes(n))
	.filter(n => only.length === 0 || only.includes(n))
	.sort();
for (const name of names) {
	const d = db.getSiblingDB(name);
	out[name] = {};
	for (const coll of d.getCollectionNames().sort()) {
		out[name][coll] = d.getCollection(coll).find().sort({_id: 1}).toArray();
	}
}
print(EJSON.stringify(out, {relaxed: false}));
`

// Dump returns the contents of the user databases of a MongoDB container, or
// of dbs only when given, in a form two servers can be compared on.
func Dump(ctx context.Context, t *testing.T, container testcontainers.Container, dbs ...string) string {
	t.Helper()
	if dbs == nil {
		dbs = []string{}
	}
	list, err := json.Marshal(dbs)
	if err != nil {
		t.Fatalf("marshal database list: %v", err)
	}
	return Mongosh(ctx, t, container, strings.Replace(dumpScript, "DBS", string(list), 1))
}
