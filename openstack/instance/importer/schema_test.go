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

	"github.com/PlakarKorp/integrations/openstack/common/keystonetest"
	"github.com/PlakarKorp/integrations/openstack/instance"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImporterSchemaAccepts(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	schema := compileSchema(t)

	tests := []struct {
		name string
		edit map[string]string
		drop []string
	}{
		{name: "server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := editedParams(cloud.AuthURL, "openstack-instance://server-1", tt.edit, tt.drop)

			assert.NoError(t, schema.Validate(asAny(p)))

			// NewImporter must agree, so the schema accepts what would work at startup.
			_, err := NewImporter(t.Context(), nil, instance.Protocol, p)
			assert.NoError(t, err)
		})
	}
}

func TestImporterSchemaRejects(t *testing.T) {
	cloud := keystonetest.NewCloud(t)
	schema := compileSchema(t)

	tests := []struct {
		name string
		edit map[string]string
		drop []string
	}{
		{name: "no location", drop: []string{"location"}},
		{name: "location with a slash", edit: map[string]string{"location": "openstack-instance://server-1/x"}},
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
			p := editedParams(cloud.AuthURL, "openstack-instance://server-1", tt.edit, tt.drop)

			assert.Error(t, schema.Validate(asAny(p)))

			// NewImporter must agree, so the schema rejects what would fail at startup.
			_, err := NewImporter(t.Context(), nil, instance.Protocol, p)
			assert.Error(t, err)
		})
	}
}

func TestImporterSchemaRejectsUnknownSettings(t *testing.T) {
	p := params("http://keystone/v3/", "openstack-instance://server-1")
	p["openstack_page_size"] = "10"
	assert.Error(t, compileSchema(t).Validate(asAny(p)))
}

// editedParams is valid params with edit applied and drop deleted.
func editedParams(authURL, location string, edit map[string]string, drop []string) map[string]string {
	p := params(authURL, location)
	maps.Copy(p, edit)
	for _, k := range drop {
		delete(p, k)
	}
	return p
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

func asAny(params map[string]string) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}
