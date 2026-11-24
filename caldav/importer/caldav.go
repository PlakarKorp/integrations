package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	"github.com/PlakarKorp/integrations/caldav/oauth2utils"
	"github.com/studio-b12/gowebdav"
	"golang.org/x/oauth2"
)

type CaldavImporter struct {
	opts *integration.Options

	client *gowebdav.Client
	url    string // The URL of the CalDAV server
}

func NewCaldavImporter(ctx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Importer, error) {

	// Example google calendar CalDAV URL:
	//url := "https://apidata.googleusercontent.com/caldav/v2/EMAIL@gmail.com/events/"

	location, found := config["location"]
	if !found {
		return nil, errors.New("missing 'location' in configuration")
	}

	username, ok := config["username"]
	if !ok {
		return nil, errors.New("missing 'username' in configuration")
	}
	provider, isOAuthClient := config["name"]

	var client *gowebdav.Client
	var url string
	if !isOAuthClient {
		password, ok := config["password"]
		if !ok {
			return nil, errors.New("missing 'password' in configuration")
		}
		url = strings.TrimPrefix(location, "caldav://")
		client = gowebdav.NewClient(url, username, password)
	} else { // OAuth2 client setup

		clientID, ok := config["client_id"]
		if !ok {
			return nil, errors.New("missing 'client_id' in configuration")
		}
		clientSecret, ok := config["client_secret"]
		if !ok {
			return nil, errors.New("missing 'client_secret' in configuration")
		}
		serviceScopes, err := oauth2utils.GetOAuth2Scopes(provider)
		if err != nil {
			return nil, fmt.Errorf("error getting OAuth2 scopes for provider '%s': %w", provider, err)
		}
		endpoint, err := oauth2utils.GetOAuth2Endpoint(provider)
		if err != nil {
			return nil, fmt.Errorf("error getting OAuth2 endpoint for provider '%s': %w", provider, err)
		}

		calOAuthProvider := oauth2utils.OAuthProvider{
			Name: provider,
			Config: &oauth2.Config{
				ClientID:     clientID,     // client ID (provided by the plakar app (production) or by the user directly in a personal app)
				ClientSecret: clientSecret, // client secret (same as above)
				RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
				Scopes:       serviceScopes, // e.g., "https://www.googleapis.com/auth/calendar"
				Endpoint:     endpoint,      // e.g., google.Endpoint for Google Calendar
			},
		}

		url = oauth2utils.GetOAuth2Url(provider, username)

		client = calOAuthProvider.GetClient(url) // maybe not using the url directly... the url could be built from the username
	}

	return &CaldavImporter{
		opts: opts,

		client: client,
		url:    url,
	}, nil
}

func (c *CaldavImporter) Origin() string        { return c.url }
func (c *CaldavImporter) Type() string          { return "caldav" }
func (c *CaldavImporter) Root() string          { return "/" }
func (c *CaldavImporter) Flags() location.Flags { return 0 }

func (c *CaldavImporter) Ping(ctx context.Context) error {
	_, err := c.client.ReadDir("/")
	return err
}

func (c *CaldavImporter) Import(ctx context.Context, records chan<- *integration.Record, results <-chan *integration.Result) (map[string]string, error) {
	defer close(records)

	entries, err := c.client.ReadDir("/")
	if err != nil {
		records <- integration.NewError("/", fmt.Errorf("error reading directory: %w", err))
		return nil, nil //nolint:nilnil // no import metadata
	}
	records <- integration.NewRecord("/", "", integration.FileInfo{
		Lname:    "/",
		Lsize:    0,
		Lmode:    os.ModeDir | 0755,
		LmodTime: entries[0].ModTime(),
	}, nil, nil)
	if len(entries) == 0 {
		records <- integration.NewError("/", errors.New("no entries found in the root directory"))
		return nil, nil //nolint:nilnil // no import metadata
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".ics") {
			data, err := c.client.Read(entry.Name())
			if err != nil {
				records <- integration.NewError("/"+entry.Name(), fmt.Errorf("error reading file %s: %w", entry.Name(), err))
				continue
			}

			rd := bytes.NewReader(data)

			records <- integration.NewRecord("/"+entry.Name(), "", integration.FileInfo{
				Lname:    entry.Name(),
				Lsize:    entry.Size(),
				Lmode:    entry.Mode(),
				LmodTime: entry.ModTime(),
			}, nil, func() (io.ReadCloser, error) {
				return io.NopCloser(rd), nil
			})
		}
	}

	return nil, nil //nolint:nilnil // no import metadata
}

func (c *CaldavImporter) Close(ctx context.Context) error {
	return nil
}
