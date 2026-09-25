package openstack

import (
	"errors"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

const defaultDomain = "Default"

var (
	ErrMissingAuthURL = errors.New("openstack_auth_url is required")
	ErrMissingAuth    = errors.New("an application credential or openstack_username and openstack_password are required")
	ErrPartialAppCred = errors.New("openstack_application_credential_id and openstack_application_credential_secret must be set together")
	ErrMissingProject = errors.New("openstack_project_id or openstack_project_name is required with password authentication")
)

type config struct {
	auth gophercloud.AuthOptions
	// regions restricts the scan to these catalog regions; empty scans them all.
	regions []string
}

func parseConfig(params map[string]string) (*config, error) {
	authURL := params["openstack_auth_url"]
	if authURL == "" {
		return nil, ErrMissingAuthURL
	}

	cfg := &config{
		auth: gophercloud.AuthOptions{
			IdentityEndpoint: authURL,
			AllowReauth:      true,
		},
		regions: paramList(params, "openstack_region"),
	}

	appCredID := params["openstack_application_credential_id"]
	appCredSecret := params["openstack_application_credential_secret"]
	if appCredID != "" || appCredSecret != "" {
		if appCredID == "" || appCredSecret == "" {
			return nil, ErrPartialAppCred
		}
		// Application credentials are bound to a project; Keystone rejects an explicit scope.
		cfg.auth.ApplicationCredentialID = appCredID
		cfg.auth.ApplicationCredentialSecret = appCredSecret
		return cfg, nil
	}

	username := params["openstack_username"]
	password := params["openstack_password"]
	if username == "" || password == "" {
		return nil, ErrMissingAuth
	}

	domain := params["openstack_domain_name"]
	if domain == "" {
		domain = defaultDomain
	}

	cfg.auth.Username = username
	cfg.auth.Password = password
	cfg.auth.DomainName = domain

	projectID := params["openstack_project_id"]
	projectName := params["openstack_project_name"]
	switch {
	case projectID != "":
		cfg.auth.Scope = &gophercloud.AuthScope{ProjectID: projectID}
	case projectName != "":
		cfg.auth.Scope = &gophercloud.AuthScope{ProjectName: projectName, DomainName: domain}
	default:
		return nil, ErrMissingProject
	}

	return cfg, nil
}

func paramList(params map[string]string, key string) []string {
	var out []string
	for item := range strings.SplitSeq(params[key], ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
