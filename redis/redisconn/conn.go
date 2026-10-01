package redisconn

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

// ConnConfig holds redis-cli connection settings.
type ConnConfig struct {
	Host        string
	Port        string
	Username    string
	Password    string
	Database    string
	TLS         bool
	InsecureTLS bool
	CACert      string
	Cert        string
	Key         string
	BinDir      string
	ClientBin   string
}

func ParseConnConfig(config map[string]string) (ConnConfig, error) {
	cc := ConnConfig{Host: "127.0.0.1", Port: "6379", ClientBin: "redis-cli"}
	if location := config["location"]; location != "" {
		if err := parseURI(location, &cc); err != nil {
			return cc, err
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
	if v := config["database"]; v != "" {
		cc.Database = v
	}
	if v := config["redis_bin_dir"]; v != "" {
		cc.BinDir = v
	}
	if v := config["redis_cli"]; v != "" {
		cc.ClientBin = v
	}
	var err error
	if cc.TLS, err = boolOpt(config, "tls", cc.TLS); err != nil {
		return cc, err
	}
	if cc.InsecureTLS, err = boolOpt(config, "insecure_tls", false); err != nil {
		return cc, err
	}
	if v := config["ca_cert"]; v != "" {
		cc.CACert = v
	}
	if v := config["cert"]; v != "" {
		cc.Cert = v
	}
	if v := config["key"]; v != "" {
		cc.Key = v
	}
	return cc, nil
}

func boolOpt(config map[string]string, key string, def bool) (bool, error) {
	v := config[key]
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid value for %s: %w", key, err)
	}
	return b, nil
}

func parseURI(raw string, cc *ConnConfig) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid Redis URI %q: %w", raw, err)
	}
	switch u.Scheme {
	case "redis":
	case "rediss":
		cc.TLS = true
	default:
		return fmt.Errorf("unsupported URI scheme %q: expected redis:// or rediss://", u.Scheme)
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
		if pass, ok := u.User.Password(); ok {
			cc.Password = pass
		}
	}
	if db := strings.TrimPrefix(u.Path, "/"); db != "" {
		cc.Database = db
	}
	return nil
}

func (cc ConnConfig) Bin() string {
	bin := cc.ClientBin
	if bin == "" {
		bin = "redis-cli"
	}
	if cc.BinDir == "" {
		return bin
	}
	return filepath.Join(cc.BinDir, bin)
}

func (cc ConnConfig) Args(extra ...string) []string {
	args := []string{"-h", cc.Host, "-p", cc.Port}
	if cc.Username != "" {
		args = append(args, "--user", cc.Username)
	}
	if cc.Database != "" {
		args = append(args, "-n", cc.Database)
	}
	if cc.TLS {
		args = append(args, "--tls")
	}
	if cc.InsecureTLS {
		args = append(args, "--insecure")
	}
	if cc.CACert != "" {
		args = append(args, "--cacert", cc.CACert)
	}
	if cc.Cert != "" {
		args = append(args, "--cert", cc.Cert)
	}
	if cc.Key != "" {
		args = append(args, "--key", cc.Key)
	}
	return append(args, extra...)
}

func (cc ConnConfig) Env() []string {
	env := os.Environ()
	if cc.Password != "" {
		env = append(env, "REDISCLI_AUTH="+cc.Password)
	}
	return env
}

func (cc ConnConfig) Ping(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, cc.Bin(), cc.Args("PING")...)
	cmd.Env = cc.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("redis ping failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != "PONG" {
		return fmt.Errorf("unexpected Redis PING response: %q", strings.TrimSpace(string(out)))
	}
	return nil
}

func (cc ConnConfig) Origin(proto string) string {
	if cc.Database != "" {
		return proto + "://" + cc.Host + ":" + cc.Port + "/" + cc.Database
	}
	return proto + "://" + cc.Host + ":" + cc.Port
}
