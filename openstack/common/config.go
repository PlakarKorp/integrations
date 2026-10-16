package common

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
)

const defaultDomain = "Default"

// UploadMode selects how UploadTempImage gets an image's bytes into Glance.
type UploadMode string

const (
	// ModeDirect is a single unparallelized PUT via Glance's imagedata API.
	ModeDirect UploadMode = "direct"
	// ModeStaged splits the upload into parallel Swift segments, then has
	// Glance pull the assembled object server-side. Requires Swift in the
	// target region's catalog.
	ModeStaged UploadMode = "staged"
)

var (
	ErrMissingAuthURL = errors.New("openstack_auth_url is required")
	ErrMissingAuth    = errors.New("an application credential or openstack_username and openstack_password are required")
	ErrPartialAppCred = errors.New("openstack_application_credential_id and openstack_application_credential_secret must be set together")
	ErrMissingProject = errors.New("openstack_project_id or openstack_project_name is required with password authentication")
	ErrMissingRegion  = errors.New("openstack_region is required")
	ErrManyRegions    = errors.New("openstack_region: bad value: a connector takes a single region")
)

type Config struct {
	auth gophercloud.AuthOptions
	// regions restricts the scan to these catalog regions; empty scans them all.
	regions []string
	// UploadMode is how UploadTempImage uploads a disk's bytes.
	UploadMode UploadMode
	// objectStorageRegion is where staged uploads find Swift; empty means the
	// connector's own region.
	objectStorageRegion string
}

func ParseConfig(params map[string]string) (*Config, error) {
	authURL := params["openstack_auth_url"]
	if authURL == "" {
		return nil, ErrMissingAuthURL
	}

	mode := UploadMode(params["openstack_upload_mode"])
	if mode == "" {
		mode = ModeDirect
	}
	if mode != ModeDirect && mode != ModeStaged {
		return nil, fmt.Errorf("openstack_upload_mode: bad value: %q", mode)
	}

	storageRegion, set := params["openstack_object_storage_region"]
	storageRegion = strings.TrimSpace(storageRegion)
	if set && (storageRegion == "" || strings.Contains(storageRegion, ",")) {
		return nil, fmt.Errorf("openstack_object_storage_region: bad value: %q, expected a single region", storageRegion)
	}

	cfg := &Config{
		auth: gophercloud.AuthOptions{
			IdentityEndpoint: authURL,
			AllowReauth:      true,
		},
		regions:             paramList(params, "openstack_region"),
		UploadMode:          mode,
		objectStorageRegion: storageRegion,
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

func paramList(params map[string]string, key string) []string {
	var out []string
	for item := range strings.SplitSeq(params[key], ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
