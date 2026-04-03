package azblob

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	importer.Register("azblob", 0, NewImporter)
}

type azblobImporter struct {
	containerName string
	path          string
	base          string
	endpoint      string

	accountName      string
	accountKey       string
	connectionString string
	noAuth           bool

	client *azblob.Client
}

func NewImporter(ctx context.Context, _ *connectors.Options, proto string, params map[string]string) (importer.Importer, error) {
	container, prefix, endpoint, accountName, accountKey, connectionString, noAuth, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	imp := &azblobImporter{
		containerName:    container,
		path:             prefix,
		base:             "/" + prefix,
		endpoint:         endpoint,
		accountName:      accountName,
		accountKey:       accountKey,
		connectionString: connectionString,
		noAuth:           noAuth,
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
			return fmt.Errorf("no_auth requires endpoint")
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

	default:
		return fmt.Errorf("missing credentials: provide connection_string, or account_name/account_key, or no_auth=true with endpoint")
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

func (g *azblobImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
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
			return err
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

			fi := objects.FileInfo{
				Lname:    path.Base(name),
				Lsize:    size,
				Lmode:    0644,
				LmodTime: modTime,
				// No real Azure equivalent for GCS Owner string here in blob listing.
				// Leave empty unless you want to populate from metadata/tags.
				Lusername: "",
			}

			blobName := name
			records <- connectors.NewRecord(fullpath, "", fi, nil, func() (io.ReadCloser, error) {
				resp, err := g.client.DownloadStream(ctx, g.containerName, blobName, nil)
				if err != nil {
					return nil, err
				}
				return resp.Body, nil
			})
		}
	}

	return nil
}

func (g *azblobImporter) Close(ctx context.Context) error {
	return nil
}

func toPtr[T any](v T) *T {
	return &v
}
