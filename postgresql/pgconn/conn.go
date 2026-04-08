package pgconn

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// ConnConfig holds the connection parameters shared by all PostgreSQL connectors.
type ConnConfig struct {
	Host     string
	Port     string
	Username string
	Password string

	SSLMode     string // disable, allow, prefer, require, verify-ca, verify-full
	SSLCert     string // path to client certificate file (PEM)
	SSLKey      string // path to client private key file (PEM)
	SSLRootCert string // path to root CA certificate file (PEM)
}

// ParseConnConfig parses host, port, username, and password from the "location"
// URI and from standalone config keys. Standalone keys override URI components.
// The path component of the URI (i.e. the database name) is returned separately
// so callers can handle it as appropriate (use it, reject it, or ignore it).
func ParseConnConfig(config map[string]string) (ConnConfig, string, error) {
	c := ConnConfig{
		Host: "localhost",
		Port: "5432",
	}
	var dbPath string

	if loc, ok := config["location"]; ok && loc != "" {
		u, err := url.Parse(loc)
		if err != nil {
			return ConnConfig{}, "", fmt.Errorf("invalid location: %w", err)
		}
		if u.Hostname() != "" {
			c.Host = u.Hostname()
		}
		if u.Port() != "" {
			c.Port = u.Port()
		}
		if u.User != nil {
			if u.User.Username() != "" {
				c.Username = u.User.Username()
			}
			if p, ok := u.User.Password(); ok {
				c.Password = p
			}
		}
		if u.Path != "" && u.Path != "/" {
			dbPath = u.Path[1:] // strip leading /
		}
	}

	// Standalone fields override URI components.
	if h, ok := config["host"]; ok && h != "" {
		c.Host = h
	}
	if p, ok := config["port"]; ok && p != "" {
		c.Port = p
	}
	if u, ok := config["username"]; ok && u != "" {
		c.Username = u
	}
	if p, ok := config["password"]; ok && p != "" {
		c.Password = p
	}
	if v, ok := config["ssl_mode"]; ok && v != "" {
		switch v {
		case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
			c.SSLMode = v
		default:
			return ConnConfig{}, "", fmt.Errorf("ssl_mode: invalid value %q (accepted: disable, allow, prefer, require, verify-ca, verify-full)", v)
		}
	}
	if v, ok := config["ssl_cert"]; ok && v != "" {
		c.SSLCert = v
	}
	if v, ok := config["ssl_key"]; ok && v != "" {
		c.SSLKey = v
	}
	if v, ok := config["ssl_root_cert"]; ok && v != "" {
		c.SSLRootCert = v
	}

	return c, dbPath, nil
}

// Args returns the common connection flags shared by all PostgreSQL client
// tools: -h host -p port -w, plus -U username when one is configured.
func (c ConnConfig) Args() []string {
	args := []string{"-h", c.Host, "-p", c.Port, "-w"}
	if c.Username != "" {
		args = append(args, "-U", c.Username)
	}
	return args
}

// Env returns the current process environment with PostgreSQL authentication
// and TLS variables injected when configured.
func (c ConnConfig) Env() []string {
	env := os.Environ()
	if c.Password != "" {
		env = append(env, "PGPASSWORD="+c.Password)
	}
	if c.SSLMode != "" {
		env = append(env, "PGSSLMODE="+c.SSLMode)
	}
	if c.SSLCert != "" {
		env = append(env, "PGSSLCERT="+c.SSLCert)
	}
	if c.SSLKey != "" {
		env = append(env, "PGSSLKEY="+c.SSLKey)
	}
	if c.SSLRootCert != "" {
		env = append(env, "PGSSLROOTCERT="+c.SSLRootCert)
	}
	return env
}

// DSN builds a PostgreSQL connection string for the given database.
func (c ConnConfig) DSN(dbname string) string {
	u := &url.URL{
		Scheme: "postgresql",
		Host:   c.Host + ":" + c.Port,
		Path:   "/" + dbname,
	}
	if c.Username != "" {
		if c.Password != "" {
			u.User = url.UserPassword(c.Username, c.Password)
		} else {
			u.User = url.User(c.Username)
		}
	}
	q := url.Values{}
	if c.SSLMode != "" {
		q.Set("sslmode", c.SSLMode)
	}
	if c.SSLCert != "" {
		q.Set("sslcert", c.SSLCert)
	}
	if c.SSLKey != "" {
		q.Set("sslkey", c.SSLKey)
	}
	if c.SSLRootCert != "" {
		q.Set("sslrootcert", c.SSLRootCert)
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// Open returns a *sql.DB connected to the given database.
func (c ConnConfig) Open(dbname string) (*sql.DB, error) {
	return sql.Open("pgx", c.DSN(dbname))
}

// Ping verifies connectivity by running a ping against the server.
// If connectDB is empty, "postgres" is used.
func (c ConnConfig) Ping(ctx context.Context, connectDB string) error {
	if connectDB == "" {
		connectDB = "postgres"
	}
	db, err := c.Open(connectDB)
	if err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	return nil
}
