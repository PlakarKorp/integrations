package inventory

import (
	"bytes"
	"testing"

	"github.com/PlakarKorp/integrations-private/openstack/common"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const keystoneURL = "http://keystone/v3/"

func TestSchema(t *testing.T) {
	schema := compileSchema(t)

	tests := []struct {
		name   string
		params map[string]string
		valid  bool
	}{
		{
			name: "application credential",
			params: map[string]string{
				"openstack_auth_url":                      keystoneURL,
				"openstack_application_credential_id":     "id",
				"openstack_application_credential_secret": "secret",
			},
			valid: true,
		},
		{
			name: "application credential with a stray username",
			params: map[string]string{
				"openstack_auth_url":                      keystoneURL,
				"openstack_application_credential_id":     "id",
				"openstack_application_credential_secret": "secret",
				"openstack_username":                      "user",
			},
			valid: true,
		},
		{
			name: "password with project id",
			params: map[string]string{
				"openstack_auth_url":   keystoneURL,
				"openstack_username":   "user",
				"openstack_password":   "pass",
				"openstack_project_id": "p1",
			},
			valid: true,
		},
		{
			name: "password with project name and domain",
			params: map[string]string{
				"openstack_auth_url":     keystoneURL,
				"openstack_username":     "user",
				"openstack_password":     "pass",
				"openstack_project_name": "tenant",
				"openstack_domain_name":  "corp",
			},
			valid: true,
		},
		{
			name: "no auth url",
			params: map[string]string{
				"openstack_username":     "user",
				"openstack_password":     "pass",
				"openstack_project_name": "tenant",
			},
			valid: false,
		},
		{
			name: "no credentials",
			params: map[string]string{
				"openstack_auth_url": keystoneURL,
			},
			valid: false,
		},
		{
			name: "password without username",
			params: map[string]string{
				"openstack_auth_url":     keystoneURL,
				"openstack_password":     "pass",
				"openstack_project_name": "tenant",
			},
			valid: false,
		},
		{
			name: "password without project",
			params: map[string]string{
				"openstack_auth_url": keystoneURL,
				"openstack_username": "user",
				"openstack_password": "pass",
			},
			valid: false,
		},
		{
			name: "application credential without secret",
			params: map[string]string{
				"openstack_auth_url":                  keystoneURL,
				"openstack_application_credential_id": "id",
			},
			valid: false,
		},
		{
			name: "application credential without id",
			params: map[string]string{
				"openstack_auth_url":                      keystoneURL,
				"openstack_application_credential_secret": "secret",
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := schema.Validate(instance(tt.params))
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}

			// ParseConfig must agree, so the schema rejects what would fail at startup.
			_, err = common.ParseConfig(tt.params)
			assert.Equal(t, tt.valid, err == nil, "ParseConfig: %v", err)
		})
	}
}

func TestSchemaRejectsUnknownSettings(t *testing.T) {
	err := compileSchema(t).Validate(instance(map[string]string{
		"openstack_auth_url":                      keystoneURL,
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": "secret",
		"openstack_page_size":                     "10",
	}))
	assert.Error(t, err)
}

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(Schema))
	require.NoError(t, err)

	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource("schema.json", doc))
	schema, err := c.Compile("schema.json")
	require.NoError(t, err)
	return schema
}

// instance converts params to the generic JSON value the validator expects.
func instance(params map[string]string) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}
