package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Passwords and secrets may legitimately start or end with spaces.
func TestParseConfigKeepsSecretsAsGiven(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"openstack_auth_url":   "http://keystone/v3/",
		"openstack_username":   "user-a",
		"openstack_password":   " pass word ",
		"openstack_project_id": "p1",
	})
	require.NoError(t, err)
	assert.Equal(t, " pass word ", cfg.auth.Password)

	cfg, err = ParseConfig(map[string]string{
		"openstack_auth_url":                      "http://keystone/v3/",
		"openstack_application_credential_id":     "id",
		"openstack_application_credential_secret": " s3cret ",
	})
	require.NoError(t, err)
	assert.Equal(t, " s3cret ", cfg.auth.ApplicationCredentialSecret)
}
