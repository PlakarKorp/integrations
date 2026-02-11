/*
 * Copyright (c) 2026 Gilles DUBOIS
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package proxmox

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const DefaultDumpDir = "/var/lib/vz/dump"

const (
	ModeLocal  = "local"
	ModeRemote = "remote"
)

const (
	ConnMethodPassword = "password"
	ConnMethodIdentity = "identity"
)

type Config struct {
	Location *url.URL
	Host     string

	Mode              string
	ConnMethod        string
	ConnUsername      string
	ConnPassword      string
	ConnIdentityFile  string
	BackupCompression string
	BackupMode        string
	Node              string
	Cleanup           bool
	RestoreVMID       *int
}

func ParseConfig(config map[string]string) (*Config, error) {
	loc, ok := config["location"]
	if !ok || strings.TrimSpace(loc) == "" {
		return nil, fmt.Errorf("missing location")
	}

	parsed, err := url.Parse(loc)
	if err != nil {
		return nil, fmt.Errorf("invalid location: %w", err)
	}

	host := parsed.Host
	if host == "" {
		host = parsed.Path
	}
	if host == "" {
		return nil, fmt.Errorf("missing host in location")
	}

	mode := strings.TrimSpace(config["mode"])
	if mode == "" {
		return nil, fmt.Errorf("missing mode")
	}
	if mode != ModeLocal && mode != ModeRemote {
		return nil, fmt.Errorf("invalid mode: %s", mode)
	}

	cfg := &Config{
		Location: parsed,
		Host:     host,
		Mode:     mode,
	}

	if cfg.Mode == ModeRemote {
		cfg.ConnMethod = strings.TrimSpace(config["conn_method"])
		if cfg.ConnMethod == "" {
			return nil, fmt.Errorf("missing conn_method")
		}
		if cfg.ConnMethod != ConnMethodPassword && cfg.ConnMethod != ConnMethodIdentity {
			return nil, fmt.Errorf("invalid conn_method: %s", cfg.ConnMethod)
		}

		cfg.ConnUsername = strings.TrimSpace(config["conn_username"])
		if cfg.ConnUsername == "" {
			return nil, fmt.Errorf("missing conn_username")
		}

		switch cfg.ConnMethod {
		case ConnMethodPassword:
			cfg.ConnPassword = config["conn_password"]
			if cfg.ConnPassword == "" {
				return nil, fmt.Errorf("missing conn_password")
			}
		case ConnMethodIdentity:
			cfg.ConnIdentityFile = strings.TrimSpace(config["conn_identity_file"])
			if cfg.ConnIdentityFile == "" {
				return nil, fmt.Errorf("missing conn_identity_file")
			}
			cfg.ConnIdentityFile, err = expandPath(cfg.ConnIdentityFile)
			if err != nil {
				return nil, err
			}
		}
	}

	cfg.BackupCompression = strings.TrimSpace(config["backup_compression"])
	if cfg.BackupCompression == "" {
		cfg.BackupCompression = "0"
	}

	cfg.BackupMode = strings.TrimSpace(config["backup_mode"])
	if cfg.BackupMode == "" {
		cfg.BackupMode = "snapshot"
	}

	if vmidStr, ok := config["vmid"]; ok {
		vmidStr = strings.TrimSpace(vmidStr)
		if vmidStr != "" {
			vmid, err := strconv.Atoi(vmidStr)
			if err != nil {
				return nil, fmt.Errorf("invalid vmid: %s", vmidStr)
			}
			cfg.RestoreVMID = &vmid
		}
	}

	cfg.Node = strings.TrimSpace(config["node"])

	cleanup, err := parseBool(config, "cleanup", false)
	if err != nil {
		return nil, err
	}
	cfg.Cleanup = cleanup

	return cfg, nil
}

func (c *Config) Origin() string {
	if c.Host != "" {
		return c.Host
	}
	return "local"
}

func parseBool(config map[string]string, key string, defaultValue bool) (bool, error) {
	value := strings.TrimSpace(config[key])
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid %s value: %s", key, value)
	}
	return parsed, nil
}

func expandPath(path string) (string, error) {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		if strings.HasPrefix(path, "~/") {
			return filepath.Join(home, path[2:]), nil
		}
	}
	return path, nil
}
