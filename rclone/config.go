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
