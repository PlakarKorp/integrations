package azblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
)

func init() {
	_ = integration.RegisterImporter("azblob", 0, NewImporter)
}

type azblobImporter struct {
	containerName string
	path          string
	base          string
	endpoint      string

	accountName             string
	accountKey              string
	connectionString        string
	noAuth                  bool
	useManagedIdentity      bool
	managedIdentityClientID string

	client *azblob.Client
}

func NewImporter(ctx context.Context, _ *integration.Options, proto string, params map[string]string) (integration.Importer, error) {
	container, prefix, endpoint, accountName, accountKey, connectionString, noAuth, useManagedIdentity, managedIdentityClientID, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	imp := &azblobImporter{
		containerName:           container,
		path:                    prefix,
		base:                    "/" + prefix,
		endpoint:                endpoint,
		accountName:             accountName,
		accountKey:              accountKey,
		connectionString:        connectionString,
		noAuth:                  noAuth,
		useManagedIdentity:      useManagedIdentity,
		managedIdentityClientID: managedIdentityClientID,
	}

	if err := imp.connect(ctx); err != nil {
		return nil, err
	}

	return imp, nil
}

func (g *azblobImporter) connect(ctx context.Context) error {
	if g.client != nil {
		return nil
	}

	switch {
	case g.connectionString != "":
		client, err := azblob.NewClientFromConnectionString(g.connectionString, nil)
		if err != nil {
			return err
		}
		g.client = client
		return nil

	case g.noAuth:
		if g.endpoint == "" {
			return errors.New("no_auth requires endpoint")
		}
		client, err := azblob.NewClientWithNoCredential(strings.TrimRight(g.endpoint, "/")+"/", nil)
		if err != nil {
			return err
		}
		g.client = client
		return nil

	case g.accountName != "" && g.accountKey != "":
		cred, err := azblob.NewSharedKeyCredential(g.accountName, g.accountKey)
		if err != nil {
			return err
		}

		serviceURL := g.endpoint
		if serviceURL == "" {
			serviceURL = fmt.Sprintf("https://%s.blob.core.windows.net", g.accountName)
		}

		client, err := azblob.NewClientWithSharedKeyCredential(strings.TrimRight(serviceURL, "/")+"/", cred, nil)
		if err != nil {
			return err
		}
		g.client = client
		return nil

	case g.useManagedIdentity:
		serviceURL := g.endpoint
		if serviceURL == "" {
			if g.accountName == "" {
				return errors.New("managed identity requires endpoint or account_name")
			}
			serviceURL = fmt.Sprintf("https://%s.blob.core.windows.net", g.accountName)
		}

		opts := &azidentity.ManagedIdentityCredentialOptions{}
		if g.managedIdentityClientID != "" {
			opts.ID = azidentity.ClientID(g.managedIdentityClientID)
		}

		cred, err := azidentity.NewManagedIdentityCredential(opts)
		if err != nil {
			return err
		}

		client, err := azblob.NewClient(strings.TrimRight(serviceURL, "/")+"/", cred, nil)
		if err != nil {
			return err
		}
		g.client = client
		return nil

	default:
		return errors.New("missing credentials: provide connection_string, or account_name/account_key, or no_auth=true with endpoint, or use_managed_identity=true with endpoint or account_name")
	}
}

func (g *azblobImporter) Origin() string {
	if g.endpoint != "" {
		return g.endpoint + "/" + g.containerName
	}
	if g.accountName != "" {
		return fmt.Sprintf("%s.blob.core.windows.net/%s", g.accountName, g.containerName)
	}
	return g.containerName
}

func (g *azblobImporter) Type() string          { return "azblob" }
func (g *azblobImporter) Root() string          { return g.base }
func (g *azblobImporter) Flags() location.Flags { return 0 }

func (g *azblobImporter) Ping(ctx context.Context) error {
	pager := g.client.NewListBlobsFlatPager(g.containerName, &azblob.ListBlobsFlatOptions{
		MaxResults: toPtr(int32(1)),
	})
	if pager.More() {
		_, err := pager.NextPage(ctx)
		return err
	}
	return nil
}

func (g *azblobImporter) Import(ctx context.Context, records chan<- *integration.Record, results <-chan *integration.Result) (map[string]string, error) {
	defer close(records)

	opts := &azblob.ListBlobsFlatOptions{}
	if g.path != "" {
		prefix := strings.Trim(g.path, "/")
		if prefix != "" {
			prefix += "/"
		}
		opts.Prefix = &prefix
	}

	pager := g.client.NewListBlobsFlatPager(g.containerName, opts)

	for pager.More() {
		resp, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}

		for _, item := range resp.Segment.BlobItems {

			if item.Name == nil {
				continue
			}

			name := *item.Name

			// Keep same behavior as GCS importer:
			// skip virtual "directory marker" blobs when present.
			if strings.HasSuffix(name, "/") {
				continue
			}

			fullpath := "/" + name

			var size int64
			if item.Properties.ContentLength != nil {
				size = *item.Properties.ContentLength
			}

			var modTime time.Time
			if item.Properties.LastModified != nil {
				modTime = *item.Properties.LastModified
			}

			fi := integration.FileInfo{
				Lname:    path.Base(name),
				Lsize:    size,
				Lmode:    0644,
				LmodTime: modTime,
				// No real Azure equivalent for GCS Owner string here in blob listing.
				// Leave empty unless you want to populate from metadata/tags.
				Lusername: "",
			}

			blobName := name
			records <- integration.NewRecord(fullpath, "", fi, nil, func() (io.ReadCloser, error) {
				resp, err := g.client.DownloadStream(ctx, g.containerName, blobName, nil)
				if err != nil {
					return nil, err
				}
				return resp.Body, nil
			})
		}
	}

	return nil, nil
}

func (g *azblobImporter) Close(ctx context.Context) error {
	return nil
}

func toPtr[T any](v T) *T {
	return &v
}
