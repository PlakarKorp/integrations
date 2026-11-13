package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/oauth2"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
	"github.com/PlakarKorp/integrations/caldav/oauth2utils"
	"github.com/studio-b12/gowebdav"
)

type CaldavExporter struct {
	opts *integration.Options

	client *gowebdav.Client
	url    string // The URL of the CalDAV server
}

func NewCaldavExporter(ctx context.Context, opts *integration.Options, name string, config map[string]string) (integration.Exporter, error) {

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

	return &CaldavExporter{
		opts: opts,

		client: client,
		url:    url,
	}, nil
}

func (c *CaldavExporter) Origin() string        { return c.url }
func (c *CaldavExporter) Type() string          { return "caldav" }
func (c *CaldavExporter) Root() string          { return "/" }
func (c *CaldavExporter) Flags() location.Flags { return 0 }

func (c *CaldavExporter) Ping(ctx context.Context) error {
	_, err := c.client.ReadDir("/")
	return err
}

func (c *CaldavExporter) Export(ctx context.Context, _ map[string]string, records <-chan *integration.Record, results chan<- *integration.Result) error {
	defer close(results)

	for record := range records {
		switch {
		case record.Err != nil || record.IsXattr || record.FileInfo.IsDir():
			results <- record.Ok()
		case record.FileInfo.Mode()&os.ModeSymlink != 0:
			results <- record.Error(errors.ErrUnsupported)
		case record.FileInfo.Mode().IsRegular():
			results <- record.Error(c.StoreFile(ctx, record.Pathname, record.Reader, record.FileInfo.Size()))
		default:
			results <- record.Ok()
		}
	}

	return nil
}

func (c *CaldavExporter) StoreFile(ctx context.Context, pathname string, fp io.Reader, size int64) error {
	pathname = path.Base(pathname)

	if path.Ext(pathname) != ".ics" {
		return fmt.Errorf("unsupported file type %s, only .ics files are supported", pathname)
	}

	data, err := io.ReadAll(fp)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", pathname, err)
	}

	//TODO: look at this, it returns an error, even if the file is written successfully
	if c.client.Write(pathname, data, 0644) != nil {
		return fmt.Errorf("error writing %s: %w", pathname, err)
	}
	return nil
}

func (c *CaldavExporter) Close(ctx context.Context) error {
	return nil
}
