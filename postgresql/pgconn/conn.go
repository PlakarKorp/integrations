package pgconn

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// ConnConfig holds the connection parameters shared by all PostgreSQL connectors.
type ConnConfig struct {
	Host     string
	Port     string
	Username string
	Password string
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
			dbPath = strings.TrimPrefix(u.Path, "/")
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

	return c, dbPath, nil
}

// Env returns the current process environment with PGPASSWORD injected when
// a password is configured.
func (c ConnConfig) Env() []string {
	env := os.Environ()
	if c.Password != "" {
		env = append(env, "PGPASSWORD="+c.Password)
	}
	return env
}

// Ping verifies connectivity by running SELECT 1 against the server.
// If connectDB is empty, "postgres" is used.
func (c ConnConfig) Ping(ctx context.Context, psqlBin, connectDB string) error {
	if connectDB == "" {
		connectDB = "postgres"
	}
	args := []string{"-h", c.Host, "-p", c.Port, "-d", connectDB, "-w", "-c", "SELECT 1", "-q", "--no-psqlrc"}
	if c.Username != "" {
		args = append(args, "-U", c.Username)
	}
	cmd := exec.CommandContext(ctx, psqlBin, args...)
	cmd.Stdin = nil
	cmd.Env = c.Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ping: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
