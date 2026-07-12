/*
 * Copyright (c) 2026 Omar Polo <op@omarpolo.com>
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

package rclone

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// mapconfig implements config.Storage using a flat map.
type mapconfig struct {
	name     string
	sections map[string]map[string]string
}

func newMapConfig(name string, data map[string]string, baseConfigPath string) (*mapconfig, error) {
	sections, err := loadRcloneConfigSections(baseConfigPath)
	if err != nil {
		return nil, err
	}

	sections[name] = data

	return &mapconfig{
		name:     name,
		sections: sections,
	}, nil
}

func (m *mapconfig) Load() error                    { return nil }
func (m *mapconfig) Save() error                    { return nil }
func (m *mapconfig) Serialize() (string, error)     { return "", nil }
func (m *mapconfig) GetSectionList() []string       { return slices.Sorted(maps.Keys(m.sections)) }
func (m *mapconfig) HasSection(section string) bool { _, ok := m.sections[section]; return ok }
func (m *mapconfig) DeleteSection(section string)   { return }

func (m *mapconfig) GetKeyList(section string) []string {
	data := m.sections[section]
	if data == nil {
		return nil
	}
	return slices.Collect(maps.Keys(data))
}

func (m *mapconfig) GetValue(section, key string) (string, bool) {
	data := m.sections[section]
	if data == nil {
		return "", false
	}
	v, ok := data[key]
	return v, ok
}

func (m *mapconfig) SetValue(section, key, value string) {
	data := m.sections[section]
	if data == nil {
		data = make(map[string]string)
		m.sections[section] = data
	}
	data[key] = value
}

func (m *mapconfig) DeleteKey(section, key string) bool {
	data := m.sections[section]
	if data == nil {
		return false
	}

	_, ok := data[key]
	delete(data, key)
	return ok
}

func loadRcloneConfigSections(explicitPath string) (map[string]map[string]string, error) {
	sections := make(map[string]map[string]string)

	configPath, explicit := resolveRcloneConfigPath(explicitPath)
	if configPath == "" {
		return sections, nil
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		if explicit {
			return nil, fmt.Errorf("failed to read rclone config file %s: %w", configPath, err)
		}
		return sections, nil
	}

	var current map[string]string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if section == "" {
				current = nil
				continue
			}
			current = sections[section]
			if current == nil {
				current = make(map[string]string)
				sections[section] = current
			}
			continue
		}

		if current == nil {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		current[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	return sections, nil
}

func resolveRcloneConfigPath(explicitPath string) (string, bool) {
	if explicitPath != "" {
		return explicitPath, true
	}

	candidates := []string{}
	if path := os.Getenv("RCLONE_CONFIG"); path != "" {
		candidates = append(candidates, path)
	}
	if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
		candidates = append(candidates, filepath.Join(configDir, "rclone", "rclone.conf"))
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		candidates = append(candidates, filepath.Join(homeDir, ".config", "rclone", "rclone.conf"))
	}

	for _, path := range candidates {
		if stat, err := os.Stat(path); err == nil && !stat.IsDir() {
			return path, false
		}
	}

	return "", false
}
