package main

import (
	"context"
	"fmt"
	"golang.org/x/oauth2"
	"io"
	"io/ioutil"
	"path"
	"strings"

	"github.com/PlakarKorp/integration-caldav/oauth2utils"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/snapshot/exporter"
	"github.com/studio-b12/gowebdav"
)

type CaldavExporter struct {
	opts *exporter.Options

	client *gowebdav.Client
	url    string // The URL of the CalDAV server
}

func NewCaldavExporter(ctx context.Context, opts *exporter.Options, name string, config map[string]string) (exporter.Exporter, error) {

	// Example google calendar CalDAV URL:
	//url := "https://apidata.googleusercontent.com/caldav/v2/EMAIL@gmail.com/events/"

	location, found := config["location"]
	if !found {
		return nil, fmt.Errorf("missing 'location' in configuration")
	}
	url := strings.TrimPrefix(location, "caldav://")

	name, isOAuthClient := config["oauth2"]

	var client *gowebdav.Client
	if !isOAuthClient {
		username, ok := config["username"]
		if !ok {
			return nil, fmt.Errorf("missing 'username' in configuration")
		}
		password, ok := config["password"]
		if !ok {
			return nil, fmt.Errorf("missing 'password' in configuration")
		}
		client = gowebdav.NewClient(url, username, password)
	} else { // OAuth2 client setup

		clientID, ok := config["client_id"]
		if !ok {
			return nil, fmt.Errorf("missing 'client_id' in configuration")
		}
		clientSecret, ok := config["client_secret"]
		if !ok {
			return nil, fmt.Errorf("missing 'client_secret' in configuration")
		}
		serviceScope, ok := config["service_scope"]
		if !ok {
			return nil, fmt.Errorf("missing 'service_scope' in configuration")
		}
		endpoint, err := oauth2utils.GetOAuth2Endpoint(name)
		if err != nil {
			return nil, fmt.Errorf("error getting OAuth2 endpoint for provider '%s': %w", name, err)
		}

		calOAuthProvider := oauth2utils.OAuthProvider{
			Name: name,
			Config: &oauth2.Config{
				ClientID:     clientID,     // client ID (provided by the plakar app (production) or by the user directly in a personal app)
				ClientSecret: clientSecret, // client secret (same as above)
				RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
				Scopes:       []string{serviceScope}, // e.g., "https://www.googleapis.com/auth/calendar"
				Endpoint:     endpoint,               // e.g., google.Endpoint for Google Calendar //TODO: make the endpoint configurable
			},
		}
		client = calOAuthProvider.GetClient(url) // maybe not using the url directly... the url could be built from the username
	}

	return &CaldavExporter{
		opts: opts,

		client: client,
		url:    url,
	}, nil
}

func (c *CaldavExporter) Root(ctx context.Context) (string, error) {
	return "/", nil
}

func (c *CaldavExporter) CreateDirectory(ctx context.Context, pathname string) error {
	return nil
}

func (c *CaldavExporter) StoreFile(ctx context.Context, pathname string, fp io.Reader, size int64) error {
	pathname = path.Base(pathname)

	if path.Ext(pathname) != ".ics" {
		return fmt.Errorf("unsupported file type %s, only .ics files are supported", pathname)
	}

	data, err := ioutil.ReadAll(fp)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", pathname, err)
	}

	//TODO: look at this, it returns an error, even if the file is written successfully
	if c.client.Write(pathname, data, 0644) != nil {
		return fmt.Errorf("error writing %s: %w", pathname, err)
	}
	return nil
}

func (c *CaldavExporter) SetPermissions(ctx context.Context, pathname string, fileinfo *objects.FileInfo) error {
	return nil
}

func (c *CaldavExporter) Close(ctx context.Context) error {
	return nil
}
