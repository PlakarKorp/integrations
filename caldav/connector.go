package caldav

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/studio-b12/gowebdav"
	"golang.org/x/oauth2"
)

type CalDAV struct {
	opts *connectors.Options

	client   *gowebdav.Client
	location *url.URL
}

func New(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (*CalDAV, error) {
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
		serviceScopes, err := GetOAuth2Scopes(name)
		if err != nil {
			return nil, fmt.Errorf("error getting OAuth2 scopes for provider '%s': %w", name, err)
		}
		endpoint, err := GetOAuth2Endpoint(name)
		if err != nil {
			return nil, fmt.Errorf("error getting OAuth2 endpoint for provider '%s': %w", name, err)
		}

		calOAuthProvider := OAuthProvider{
			Name: name,
			Config: &oauth2.Config{
				ClientID:     clientID,     // client ID (provided by the plakar app (production) or by the user directly in a personal app)
				ClientSecret: clientSecret, // client secret (same as above)
				RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
				Scopes:       serviceScopes, // e.g., "https://www.googleapis.com/auth/calendar"
				Endpoint:     endpoint,      // e.g., google.Endpoint for Google Calendar
			},
		}

		url = GetOAuth2Url(name, username)

		client = calOAuthProvider.GetClient(url) // maybe not using the url directly... the url could be built from the username
	}

	return &CalDAV{
		opts: opts,

		client:   client,
		location: loc,
	}, nil
}

func NewImporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	return New(ctx, opts, name, config)
}

func NewExporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
	return New(ctx, opts, name, config)
}

func (c *CalDAV) Origin() string        { return c.location.Host }
func (c *CalDAV) Type() string          { return "caldav" }
func (c *CalDAV) Root() string          { return c.location.Path }
func (c *CalDAV) Flags() location.Flags { return 0 }

func (c *CalDAV) Ping(ctx context.Context) error {
	return nil
}

func (c *CalDAV) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	entries, err := c.client.ReadDir("/")
	if err != nil {
		return fmt.Errorf("error reading directory: %w", err)
	}

	records <- connectors.NewRecord("/", "", objects.FileInfo{
		Lname:    "/",
		Lsize:    0,
		Lmode:    os.ModeDir | 0755,
		LmodTime: entries[0].ModTime(),
	}, nil, nil)

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".ics") {
			data, err := c.client.Read(entry.Name())
			if err != nil {
				records <- connectors.NewError("/"+entry.Name(), fmt.Errorf("error reading file %s: %w", entry.Name(), err))
				continue
			}

			rd := bytes.NewReader(data)
			records <- connectors.NewRecord("/"+entry.Name(), "", objects.FileInfo{
				Lname:    entry.Name(),
				Lsize:    entry.Size(),
				Lmode:    entry.Mode(),
				LmodTime: entry.ModTime(),
			}, nil, func() (io.ReadCloser, error) {
				return io.NopCloser(rd), nil
			})
		}
	}

	return nil
}

func (c *CalDAV) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
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

func (c *CalDAV) Close(ctx context.Context) error {
	return nil
}
