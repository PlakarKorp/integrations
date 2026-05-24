package testhelpers

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// DBVariant describes a database engine variant used in parameterized tests.
type DBVariant struct {
	Name       string
	Image      string            // Docker image for the database server
	Env        map[string]string // environment variables for the container
	WaitFor    wait.Strategy     // strategy to determine when the server is ready
	Protocol   string            // Plakar protocol: "mysql" or "mysql+mariadb"
	CLI        string            // client CLI for seeding: "mysql" or "mariadb"
	Dockerfile string            // path to the plakar test image Dockerfile (from repo root)
	ImageTag   string            // Docker image name for the cached plakar container
}

// DBVariants lists all database variants exercised by the integration tests.
var DBVariants = []DBVariant{
	{
		Name:  "mysql",
		Image: "mysql:8",
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "secret",
			"MYSQL_DATABASE":      "testdb",
		},
		// MySQL logs this line exactly once when the real server is ready.
		WaitFor:    wait.ForLog("port: 3306  MySQL Community Server"),
		Protocol:   "mysql",
		CLI:        "mysql",
		Dockerfile: "tests/plakar-mysql.Dockerfile",
		ImageTag:   "plakar-mysql-test",
	},
	{
		Name:  "mariadb",
		Image: "mariadb:11",
		Env: map[string]string{
			"MARIADB_ROOT_PASSWORD": "secret",
			"MARIADB_DATABASE":      "testdb",
		},
		// MariaDB logs "ready for connections" twice: once during the temporary
		// init server and once when the real server starts. Wait for the second.
		WaitFor:    wait.ForLog("ready for connections").WithOccurrence(2),
		Protocol:   "mysql+mariadb",
		CLI:        "mariadb",
		Dockerfile: "tests/plakar-mariadb.Dockerfile",
		ImageTag:   "plakar-mariadb-test",
	},
}

// StartDBContainer starts a database container for the given variant, attached
// to net with the given network alias. The container is automatically
// terminated when the test ends.
func StartDBContainer(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork, alias string, v DBVariant) testcontainers.Container {
	t.Helper()

	req := testcontainers.ContainerRequest{
		Image:          v.Image,
		Env:            v.Env,
		ExposedPorts:   []string{"3306/tcp"},
		Networks:       []string{net.Name},
		NetworkAliases: map[string][]string{net.Name: {alias}},
		WaitingFor:     v.WaitFor,
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start %s container: %v", v.Name, err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	return container
}

// SeedDB populates testdb with representative data: two tables with rows, a
// stored procedure, and a trigger. The CLI binary (mysql or mariadb) is taken
// from the variant.
func SeedDB(ctx context.Context, t *testing.T, container testcontainers.Container, v DBVariant) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE users (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255) NOT NULL)`,
		`INSERT INTO users (name) VALUES ('alice'), ('bob'), ('carol')`,
		`CREATE TABLE orders (id INT AUTO_INCREMENT PRIMARY KEY, user_id INT NOT NULL, amount DECIMAL(10,2) NOT NULL, FOREIGN KEY (user_id) REFERENCES users(id))`,
		`INSERT INTO orders (user_id, amount) VALUES (1, 99.99), (2, 149.50), (3, 9.99)`,
		`CREATE PROCEDURE get_users() SELECT * FROM users`,
		`CREATE TRIGGER before_order_insert BEFORE INSERT ON orders FOR EACH ROW SET NEW.amount = IF(NEW.amount < 0, 0, NEW.amount)`,
	}
	for _, stmt := range stmts {
		ExecOK(ctx, t, container, v.CLI, "-uroot", "-psecret", "testdb", "-e", stmt)
	}
}

// MustQueryInt connects to the database container from the test host and runs
// a query that returns a single integer (e.g. SELECT COUNT(*) FROM ...).
// The test fails immediately on any connection or query error.
func MustQueryInt(ctx context.Context, t *testing.T, container testcontainers.Container, user, password, database, query string) int {
	t.Helper()
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "3306")
	if err != nil {
		t.Fatalf("get container mapped port: %v", err)
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true", user, password, host, port.Port(), database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open connection: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}
