package common

import (
	"os"
	"strings"
	"testing"
)

var creds = Credentials{
	Host:     "db.example",
	Port:     "27017",
	Username: "admin",
	Password: "s3cr3t",
	TLS:      true,
}

// The whole point: nothing that reaches a command line carries the password.
func TestArgsCarryNoPassword(t *testing.T) {
	for _, a := range creds.Args() {
		if strings.Contains(a, creds.Password) {
			t.Fatalf("password leaked into argv: %q", a)
		}
		if a == "--password" {
			t.Fatal("--password is still being passed on the command line")
		}
	}
}

func TestConfigFileIsPrivateAndHoldsThePassword(t *testing.T) {
	arg, cleanup, err := creds.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	path, ok := strings.CutPrefix(arg, "--config=")
	if !ok {
		t.Fatalf("unexpected arg %q", arg)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(body)), `password: "s3cr3t"`; got != want {
		t.Errorf("config = %q, want %q", got, want)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("cleanup did not remove the credentials file")
	}
}

// A password containing YAML metacharacters must not change the document.
func TestConfigFileQuotesThePassword(t *testing.T) {
	c := creds
	c.Password = `a"b\c: d` + "\n" + `evil: true`

	arg, cleanup, err := c.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	body, err := os.ReadFile(strings.TrimPrefix(arg, "--config="))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "\nevil: true") {
		t.Errorf("password broke out of its value: %q", body)
	}
}

func TestConfigFileNoPassword(t *testing.T) {
	c := creds
	c.Password = ""

	arg, cleanup, err := c.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if arg != "" {
		t.Errorf("arg = %q, want empty when there is no password", arg)
	}
}

func TestShellURI(t *testing.T) {
	got := creds.ShellURI()
	want := "mongodb://admin:s3cr3t@db.example:27017/admin?tls=true"
	if got != want {
		t.Errorf("ShellURI() = %q, want %q", got, want)
	}

	if !strings.Contains(creds.PingScript(), PingOK) {
		t.Error("PingScript does not print the marker")
	}
}
