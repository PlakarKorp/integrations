package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"golang.org/x/oauth2"

	"github.com/PlakarKorp/integrations/caldav/oauth2utils"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/studio-b12/gowebdav"
)

type CaldavExporter struct {
	opts *connectors.Options

	client   *gowebdav.Client
	location *url.URL
}

func NewCaldavExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {

	// Example google calendar CalDAV URL:
	//url := "https://apidata.googleusercontent.com/caldav/v2/EMAIL@gmail.com/events/"

	location, found := config["location"]
	if !found {
		return nil, fmt.Errorf("missing 'location' in configuration")
	}

	loc, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("failed to parse location: %w", err)
	}

	username, ok := config["username"]
	if !ok {
		return nil, fmt.Errorf("missing 'username' in configuration")
	}
	name, isOAuthClient := config["name"]

	var client *gowebdav.Client
	var url string
	if !isOAuthClient {
		password, ok := config["password"]
		if !ok {
			return nil, fmt.Errorf("missing 'password' in configuration")
		}
		url = strings.TrimPrefix(location, "caldav://")
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
		serviceScopes, err := oauth2utils.GetOAuth2Scopes(name)
		if err != nil {
			return nil, fmt.Errorf("error getting OAuth2 scopes for provider '%s': %w", name, err)
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
				Scopes:       serviceScopes, // e.g., "https://www.googleapis.com/auth/calendar"
				Endpoint:     endpoint,      // e.g., google.Endpoint for Google Calendar
			},
		}

		url = oauth2utils.GetOAuth2Url(name, username)

		client = calOAuthProvider.GetClient(url) // maybe not using the url directly... the url could be built from the username
	}

	return &CaldavExporter{
		opts: opts,

		client:   client,
		location: loc,
	}, nil
}

func (c *CaldavExporter) Origin() string        { return c.location.Host }
func (c *CaldavExporter) Type() string          { return "caldav" }
func (c *CaldavExporter) Root() string          { return c.location.Path }
func (c *CaldavExporter) Flags() location.Flags { return 0 }

func (c *CaldavExporter) Ping(ctx context.Context) error {
	return nil
}

func (c *CaldavExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for record := range records {
		if record.IsXattr || record.Err != nil || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		pathname := path.Base(record.Pathname)
		if path.Ext(pathname) != ".ics" {
			err := fmt.Errorf("unsupported file type %s, only .ics files are supported",
				pathname)
			results <- record.Error(err)
			continue
		}

		data, err := io.ReadAll(record.Reader)
		if err != nil {
			err := fmt.Errorf("failed to read file %s: %w", pathname, err)
			results <- record.Error(err)
			continue
		}

		//TODO: look at this, it returns an error, even if the file is written successfully
		if c.client.Write(pathname, data, 0644) != nil {
			err := fmt.Errorf("error writing %s: %w", pathname, err)
			results <- record.Error(err)
			continue
		}

		results <- record.Ok()
	}

	return nil
}

func (c *CaldavExporter) Close(ctx context.Context) error {
	return nil
}
