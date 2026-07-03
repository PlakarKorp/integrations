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
	"maps"
	"slices"
)

// mapconfig implements config.Storage using a flat map.
type mapconfig struct {
	name string
	data map[string]string
}

func (m *mapconfig) Load() error                    { return nil }
func (m *mapconfig) Save() error                    { return nil }
func (m *mapconfig) Serialize() (string, error)     { return "", nil }
func (m *mapconfig) GetSectionList() []string       { return []string{m.name} }
func (m *mapconfig) HasSection(section string) bool { return section == m.name }
func (m *mapconfig) DeleteSection(section string)   { return }

func (m *mapconfig) GetKeyList(section string) []string {
	if section != m.name {
		return nil
	}
	return slices.Collect(maps.Keys(m.data))
}

func (m *mapconfig) GetValue(section, key string) (string, bool) {
	if section != m.name {
		return "", false
	}
	v, ok := m.data[key]
	return v, ok
}

func (m *mapconfig) SetValue(section, key, value string) {
	if section != m.name {
		return
	}
	m.data[key] = value
}

func (m *mapconfig) DeleteKey(section, key string) bool {
	if section != m.name {
		return false
	}

	_, ok := m.data[key]
	delete(m.data, key)
	return ok
}
