// Package common holds the pieces the MongoDB importer and exporter share.
package common

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Credentials describes how to authenticate against a MongoDB deployment.
//
// The password is deliberately never rendered into a command line.  It used to
// be passed as "--password <plaintext>" to mongosh, mongodump and mongorestore,
// which puts it in /proc/<pid>/cmdline and so in every local user's ps output
// for as long as the dump runs.  The two paths below keep it out of argv:
//
//   - ConfigFile writes a 0600 YAML file for the database tools, which read a
//     password from --config.
//   - ShellURI builds a connection string for a mongosh script fed over stdin,
//     so it never touches the filesystem either.
type Credentials struct {
	Host     string
	Port     string
	Username string
	Password string
	TLS      bool
}

// Args returns the connection flags that are safe to put on a command line.
func (c Credentials) Args() []string {
	args := []string{"--host", c.Host, "--port", c.Port}
	if c.TLS {
		args = append(args, "--tls")
	}
	if c.Username != "" {
		args = append(args, "--username", c.Username)
	}
	return args
}

// ConfigFile writes the password to a temporary YAML file and returns the
// --config argument for it plus a cleanup function.  Both are no-ops when no
// password is set.
//
// mongodump and mongorestore read "password:" from the file named by --config.
func (c Credentials) ConfigFile() (arg string, cleanup func(), err error) {
	if c.Password == "" {
		return "", func() {}, nil
	}

	f, err := os.CreateTemp("", "plakar-mongo-*.yml")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating credentials file: %w", err)
	}
	name := f.Name()
	cleanup = func() { os.Remove(name) }

	if err := f.Chmod(0600); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("securing credentials file: %w", err)
	}

	// The password is quoted so that YAML metacharacters in it cannot change
	// the shape of the document.
	if _, err := fmt.Fprintf(f, "password: %s\n", yamlQuote(c.Password)); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("writing credentials file: %w", err)
	}

	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}

	return "--config=" + name, cleanup, nil
}

// yamlQuote renders s as a YAML double-quoted scalar.
//
// A newline in the password would otherwise end the value and let the rest of
// it be read as further keys in the document.
func yamlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ShellURI builds the connection string a mongosh script connects with.
func (c Credentials) ShellURI() string {
	u := &url.URL{
		Scheme: "mongodb",
		Host:   c.Host + ":" + c.Port,
		Path:   "/admin",
	}
	if c.Username != "" {
		if c.Password != "" {
			u.User = url.UserPassword(c.Username, c.Password)
		} else {
			u.User = url.User(c.Username)
		}
	}

	q := url.Values{}
	if c.TLS {
		q.Set("tls", "true")
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}

	return u.String()
}

// PingScript returns a mongosh script that connects and runs hello, printing
// the marker PingOK on success.  It is fed to "mongosh --nodb" over stdin so
// the credentials in the URI never reach argv.
func (c Credentials) PingScript() string {
	return fmt.Sprintf(`const conn = Mongo(%q);
const res = conn.getDB("admin").runCommand({ hello: 1 });
if (res.ok === 1) { print(%q); }
`, c.ShellURI(), PingOK)
}

// PingOK is the marker PingScript prints when the server answered.
const PingOK = "PLAKAR_PING_OK"
