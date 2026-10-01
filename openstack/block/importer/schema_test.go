/*
 * Copyright (c) 2026 Peeyush Aggarwal <peeyushaggarwal94@gmail.com>
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

package importer

import (
	"bytes"
	"maps"
	"testing"

	"github.com/PlakarKorp/integrations/openstack/block"
	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImporterSchema(t *testing.T) {
	cloud := keystonetest.NewCloud(t, "vol-1")
	schema := compileSchema(t)

	tests := []struct {
		name  string
		edit  map[string]string
		drop  []string
		valid bool
	}{
		{name: "volume", valid: true},
		{name: "no location", drop: []string{"location"}},
		{name: "location with a slash", edit: map[string]string{"location": "openstack-block://vol-1/x"}},
		{name: "no region", drop: []string{"openstack_region"}},
		{name: "blank region", edit: map[string]string{"openstack_region": " "}},
		{name: "two regions", edit: map[string]string{"openstack_region": "RegionOne,RegionTwo"}},
		{name: "empty credential secret", edit: map[string]string{"openstack_application_credential_secret": ""}},
		{
			name: "password with an empty project id",
			edit: map[string]string{"openstack_username": "user", "openstack_password": "pass", "openstack_project_id": ""},
			drop: []string{"openstack_application_credential_id", "openstack_application_credential_secret"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := params(cloud.AuthURL, "openstack-block://vol-1")
			maps.Copy(p, tt.edit)
			for _, k := range tt.drop {
				delete(p, k)
			}

			err := schema.Validate(instance(p))
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}

			// NewImporter must agree, so the schema rejects what would fail at startup.
			_, err = NewImporter(t.Context(), nil, block.Protocol, p)
			assert.Equal(t, tt.valid, err == nil, "NewImporter: %v", err)
		})
	}
}

func TestImporterSchemaRejectsUnknownSettings(t *testing.T) {
	p := params("http://keystone/v3/", "openstack-block://vol-1")
	p["openstack_page_size"] = "10"
	assert.Error(t, compileSchema(t).Validate(instance(p)))
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(ImporterSchema))
	require.NoError(t, err)

	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource("schema.json", doc))
	schema, err := c.Compile("schema.json")
	require.NoError(t, err)
	return schema
}

func instance(params map[string]string) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}
