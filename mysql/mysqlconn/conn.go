package mysqlconn

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ConnConfig holds the parsed connection parameters for a MySQL-compatible server.
// All CLI tools and the SQL driver share this struct.
type ConnConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	// SSLMode controls the client TLS policy.
	// Accepted values: disabled, preferred, required, verify_ca, verify_identity.
	SSLMode string
	SSLCert string
	SSLKey  string
	SSLCA   string
	// BinDir overrides the directory used to locate client binaries.
	// When empty, binaries are resolved via $PATH.
	BinDir string
	// ClientBin is the MySQL-compatible client binary name (e.g. "mysql" or "mariadb").
	// When empty, defaults to "mysql".
	ClientBin string
	// DumpBin is the dump binary name (e.g. "mysqldump" or "mariadb-dump").
	// When empty, defaults to "mysqldump".
	DumpBin string
	// ExpectedFlavor is the server type this connector is configured for
	// ("mysql" or "mariadb"). When set, Ping rejects a server of the wrong type.
	ExpectedFlavor string
}

func (cc ConnConfig) clientBin() string {
	if cc.ClientBin != "" {
		return cc.ClientBin
	}
	return "mysql"
}

func (cc ConnConfig) dumpBin() string {
	if cc.DumpBin != "" {
		return cc.DumpBin
	}
	return "mysqldump"
}

// ParseConnConfig builds a ConnConfig from the connector configuration map.
// The map may contain a "location" URI (mysql://user:pass@host:port/db) and/or
// individual override keys.  Standalone keys always take precedence over the URI.
func ParseConnConfig(config map[string]string) (ConnConfig, error) {
	cc := ConnConfig{
		Host: "127.0.0.1",
		Port: "3306",
	}

	if location, ok := config["location"]; ok && location != "" {
		if err := parseURI(location, &cc); err != nil {
			return cc, fmt.Errorf("parsing location URI: %w", err)
		}
	}

	if v := config["host"]; v != "" {
		cc.Host = v
	}
	if v := config["port"]; v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return cc, fmt.Errorf("invalid port %q: must be an integer between 1 and 65535", v)
		}
		cc.Port = v
	}
	if v := config["username"]; v != "" {
		cc.Username = v
	}
	if v := config["password"]; v != "" {
		cc.Password = v
	}
	if v := config["ssl_mode"]; v != "" {
		valid := map[string]bool{
			"disabled": true, "preferred": true, "required": true,
			"verify_ca": true, "verify_identity": true,
		}
		if !valid[strings.ToLower(v)] {
			return cc, fmt.Errorf("invalid ssl_mode %q: must be one of disabled, preferred, required, verify_ca, verify_identity", v)
		}
		cc.SSLMode = v
	}
	if v := config["ssl_cert"]; v != "" {
		cc.SSLCert = v
	}
	if v := config["ssl_key"]; v != "" {
		cc.SSLKey = v
	}
	if v := config["ssl_ca"]; v != "" {
		cc.SSLCA = v
	}
	// Accept mysql_bin_dir (MySQL connector) and mariadb_bin_dir (MariaDB connector).
	if v := config["mysql_bin_dir"]; v != "" {
		cc.BinDir = v
	}
	if v := config["mariadb_bin_dir"]; v != "" {
		cc.BinDir = v
	}

	return cc, nil
}

func parseURI(uri string, cc *ConnConfig) error {
	// Accept mysql:// and mysql+mariadb:// schemes.
	idx := strings.Index(uri, "://")
	if idx < 0 || !strings.HasPrefix(uri, "mysql") {
		return fmt.Errorf("unsupported URI scheme in %q: expected mysql:// or mysql+mariadb://", uri)
	}
	// Normalise to mysql:// so url.Parse handles it correctly.
	u, err := url.Parse("mysql" + uri[idx:])
	if err != nil {
		return fmt.Errorf("invalid URI %q: %w", uri, err)
	}
	if h := u.Hostname(); h != "" {
		cc.Host = h
	}
	if p := u.Port(); p != "" {
		cc.Port = p
	}
	if u.User != nil {
		if name := u.User.Username(); name != "" {
			cc.Username = name
		}
		if pass, ok := u.User.Password(); ok && pass != "" {
			cc.Password = pass
		}
	}
	return nil
}

// DatabaseFromConfig returns the database name from the config map.
// It checks the explicit "database" key first, then falls back to the URI path.
func DatabaseFromConfig(config map[string]string) string {
	if db := config["database"]; db != "" {
		return db
	}
	if location := config["location"]; location != "" {
		u, err := url.Parse(location)
		if err != nil {
			return ""
		}
		if p := strings.TrimPrefix(u.Path, "/"); p != "" {
			return p
		}
	}
	return ""
}

// Args returns the command-line flags common to client and dump tools.
// The password is intentionally excluded; pass it via Env() instead.
func (cc ConnConfig) Args() []string {
	args := []string{"-h", cc.Host, "-P", cc.Port}
	if cc.Username != "" {
		args = append(args, "-u", cc.Username)
	}
	if cc.SSLMode != "" {
		args = append(args, "--ssl-mode="+cc.SSLMode)
	}
	if cc.SSLCert != "" {
		args = append(args, "--ssl-cert="+cc.SSLCert)
	}
	if cc.SSLKey != "" {
		args = append(args, "--ssl-key="+cc.SSLKey)
	}
	if cc.SSLCA != "" {
		args = append(args, "--ssl-ca="+cc.SSLCA)
	}
	return args
}

// Env returns the current process environment for use as exec.Cmd.Env.
func (cc ConnConfig) Env() []string {
	return os.Environ()
}

// PasswordFileArg writes the password to a temporary MySQL option file and
// returns the --defaults-extra-file=<path> argument and a cleanup function
// that removes the file. When no password is set both are no-ops.
//
// --defaults-extra-file MUST be the first argument on the command line.
// The caller must invoke cleanup (typically via defer) once the command exits.
func (cc ConnConfig) PasswordFileArg() (arg string, cleanup func(), err error) {
	if cc.Password == "" {
		return "", func() {}, nil
	}
	f, err := os.CreateTemp("", "plakar-mysqlpwd-*.cnf")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating password file: %w", err)
	}
	name := f.Name()
	cleanup = func() { os.Remove(name) }
	if _, err := fmt.Fprintf(f, "[client]\npassword=%s\n", cc.Password); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("writing password file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return "--defaults-extra-file=" + name, cleanup, nil
}

// BinPath returns the full path to a binary.
// If BinDir is set it is joined with the binary name; otherwise the binary
// name is returned as-is for $PATH resolution.
func (cc ConnConfig) BinPath(binary string) string {
	if cc.BinDir != "" {
		return filepath.Join(cc.BinDir, binary)
	}
	return binary
}

// DSN returns a go-sql-driver/mysql data source name for the given database.
// An empty database connects without selecting one (useful for server-wide queries).
func (cc ConnConfig) DSN(database string) string {
	// Format: user:pass@tcp(host:port)/database?params
	var dsn strings.Builder
	if cc.Username != "" {
		dsn.WriteString(cc.Username)
		if cc.Password != "" {
			dsn.WriteByte(':')
			dsn.WriteString(cc.Password)
		}
		dsn.WriteByte('@')
	}
	dsn.WriteString("tcp(")
	dsn.WriteString(cc.Host)
	dsn.WriteByte(':')
	dsn.WriteString(cc.Port)
	dsn.WriteString(")/")
	dsn.WriteString(database)
	dsn.WriteString("?parseTime=true&multiStatements=true")
	if cc.SSLMode != "" {
		// go-sql-driver uses "tls" parameter; map from MySQL CLI ssl-mode names.
		tls := sslModeToTLS(cc.SSLMode)
		if tls != "" {
			dsn.WriteString("&tls=")
			dsn.WriteString(tls)
		}
	}
	return dsn.String()
}

// sslModeToTLS converts a MySQL CLI ssl-mode value to the go-sql-driver tls param.
func sslModeToTLS(mode string) string {
	switch strings.ToLower(mode) {
	case "disabled":
		return "false"
	case "preferred":
		return "preferred"
	case "required":
		return "skip-verify"
	case "verify_ca", "verify_identity":
		return "true"
	default:
		return ""
	}
}

// CheckFlavor verifies that the connected server matches the expected flavor
// ("mysql" or "mariadb"). MariaDB always includes "-MariaDB" in its VERSION()
// string; MySQL never does. Returns a clear, actionable error on mismatch so
// the user knows which protocol to use instead of getting a cryptic dump error.
func (cc ConnConfig) CheckFlavor(ctx context.Context, expectedFlavor string) error {
	pwArg, cleanup, err := cc.PasswordFileArg()
	if err != nil {
		return err
	}
	defer cleanup()
	args := cc.ArgsWithPassword(pwArg, "--batch", "--silent", "--skip-column-names", "-e", "SELECT VERSION()")
	cmd := exec.CommandContext(ctx, cc.BinPath(cc.clientBin()), args...)
	cmd.Env = cc.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("querying server version: %w: %s", err, strings.TrimSpace(string(out)))
	}
	version := strings.TrimSpace(string(out))
	isMariaDB := strings.Contains(version, "MariaDB")
	switch expectedFlavor {
	case "mariadb":
		if !isMariaDB {
			return fmt.Errorf("server version %q is MySQL, not MariaDB: use mysql:// instead of mysql+mariadb://", version)
		}
	default: // "mysql"
		if isMariaDB {
			return fmt.Errorf("server version %q is MariaDB, not MySQL: use mysql+mariadb:// instead of mysql://", version)
		}
	}
	return nil
}

// Ping verifies connectivity by running SELECT 1 against the server.
// If ExpectedFlavor is set it also checks that the server is the right type.
func (cc ConnConfig) Ping(ctx context.Context) error {
	pwArg, cleanup, err := cc.PasswordFileArg()
	if err != nil {
		return err
	}
	defer cleanup()
	args := cc.ArgsWithPassword(pwArg, "--connect-timeout=10", "--batch", "--silent", "-e", "SELECT 1")
	cmd := exec.CommandContext(ctx, cc.BinPath(cc.clientBin()), args...)
	cmd.Env = cc.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ping failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if cc.ExpectedFlavor != "" {
		return cc.CheckFlavor(ctx, cc.ExpectedFlavor)
	}
	return nil
}

// ArgsWithPassword prepends pwArg (a --defaults-extra-file argument) to the
// connection args followed by extra. If pwArg is empty it is omitted.
// --defaults-extra-file must be the first CLI argument, hence the prepend.
func (cc ConnConfig) ArgsWithPassword(pwArg string, extra ...string) []string {
	var args []string
	if pwArg != "" {
		args = append(args, pwArg)
	}
	args = append(args, cc.Args()...)
	return append(args, extra...)
}
