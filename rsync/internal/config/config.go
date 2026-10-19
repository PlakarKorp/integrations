// Package config parses the settings shared by the importer and the exporter.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integrations/rsync/internal/rsync"
)

const (
	defaultPort           = "873"
	defaultMaxConnections = 4
)

type Config struct {
	Host     string
	Port     string
	Module   string
	Dir      string // relative to the module root
	Username string
	Password string
	MaxConns int
}

func Parse(config map[string]string) (*Config, error) {
	parsed, err := url.Parse(config["location"])
	if err != nil {
		// err quotes the whole location, password included if any. Keep
		// only uerr.Err so the password does not leak.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("invalid location: %w", err)
	}
	module, dir, _ := strings.Cut(strings.TrimPrefix(parsed.Path, "/"), "/")
	if strings.Contains(config["module"], "/") {
		return nil, fmt.Errorf("invalid module %q", config["module"])
	}
	if module, err = merge("module", module, config["module"]); err != nil {
		return nil, err
	}
	if module == "" {
		return nil, fmt.Errorf("missing module in location %q", parsed.Redacted())
	}
	// root is appended to the location's path. Joining from "/" keeps a
	// ".." from leaving the module.
	dir = strings.Trim(path.Join("/", dir, config["root"]), "/")

	port, err := merge("port", parsed.Port(), config["port"])
	if err != nil {
		return nil, err
	}
	if port == "" {
		port = defaultPort
	}

	username, err := merge("username", parsed.User.Username(), config["username"])
	if err != nil {
		return nil, err
	}
	locationPassword, _ := parsed.User.Password()
	password, err := merge("password", locationPassword, config["password"])
	if err != nil {
		return nil, err
	}

	maxConns := defaultMaxConnections
	if v, ok := config["max_connections"]; ok {
		maxConns, err = strconv.Atoi(v)
		if err != nil || maxConns < 1 {
			return nil, fmt.Errorf("invalid max_connections %q", v)
		}
	}

	return &Config{
		Host:     parsed.Hostname(),
		Port:     port,
		Module:   module,
		Dir:      dir,
		Username: username,
		Password: password,
		MaxConns: maxConns,
	}, nil
}

// Root is the path of Dir in a snapshot: /<module>/<dir>.
func (c *Config) Root() string {
	return path.Join("/", c.Module, c.Dir)
}

func (c *Config) NewClient() *rsync.Client {
	return rsync.NewClient(net.JoinHostPort(c.Host, c.Port), c.Module, c.Username, c.Password, c.MaxConns)
}

func merge(name, inLocation, param string) (string, error) {
	if inLocation != "" && param != "" && inLocation != param {
		return "", fmt.Errorf("%s given both in location and as a parameter, with different values", name)
	}
	if inLocation != "" {
		return inLocation, nil
	}
	return param, nil
}
