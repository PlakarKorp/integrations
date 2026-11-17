package common

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

const defaultDomain = "Default"

var (
	ErrMissingAuthURL = errors.New("openstack_auth_url is required")
	ErrMissingAuth    = errors.New("an application credential or openstack_username and openstack_password are required")
	ErrPartialAppCred = errors.New("openstack_application_credential_id and openstack_application_credential_secret must be set together")
	ErrMissingProject = errors.New("openstack_project_id or openstack_project_name is required with password authentication")
	ErrMissingRegion  = errors.New("openstack_region is required")
	ErrManyRegions    = errors.New("openstack_region: bad value: a connector takes a single region")
	ErrBadTransfer    = errors.New(`openstack_transfer: bad value: one of "auto", "attach" or "glance"`)
)

// Transfer is how a connector moves a disk's bytes between Cinder and
// plakar: attached to the Nova server plakar runs on and read or written as
// a block device, or through a Glance image. The openstack_transfer option.
type Transfer string

const (
	// TransferAuto attaches when the host allows it, else goes through
	// Glance; the default.
	TransferAuto Transfer = "auto"
	// TransferAttach attaches, or fails the job.
	TransferAttach Transfer = "attach"
	// TransferGlance never preflights the host and never attaches: every
	// disk is an image.
	TransferGlance Transfer = "glance"
)

type Config struct {
	auth gophercloud.AuthOptions
	// regions restricts the scan to these catalog regions; empty scans them all.
	regions []string
	// Transfer is how the connectors move disks; the inventory moves none
	// and ignores it.
	Transfer Transfer
}

func ParseConfig(params map[string]string) (*Config, error) {
	authURL := params["openstack_auth_url"]
	if authURL == "" {
		return nil, ErrMissingAuthURL
	}
	transfer, err := parseTransfer(params["openstack_transfer"])
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		auth: gophercloud.AuthOptions{
			IdentityEndpoint: authURL,
			AllowReauth:      true,
		},
		regions:  paramList(params, "openstack_region"),
		Transfer: transfer,
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

// ParseConnectorConfig is ParseConfig for connectors, which work in exactly one
// region.
func ParseConnectorConfig(params map[string]string) (*Config, error) {
	region := params["openstack_region"]
	if strings.Contains(region, ",") {
		return nil, ErrManyRegions
	}
	if strings.TrimSpace(region) == "" {
		return nil, ErrMissingRegion
	}
	return ParseConfig(params)
}

// parseTransfer takes the option as written, no trimming, so it accepts
// exactly what the schemas' enum does; unset is auto.
func parseTransfer(value string) (Transfer, error) {
	switch t := Transfer(value); t {
	case "":
		return TransferAuto, nil
	case TransferAuto, TransferAttach, TransferGlance:
		return t, nil
	default:
		return "", fmt.Errorf("%w, not %q", ErrBadTransfer, value)
	}
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
