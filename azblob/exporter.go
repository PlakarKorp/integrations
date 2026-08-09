package azblob

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
)

func init() {
	exporter.Register("azblob", 0, NewExporter)
}

type azblobExporter struct {
	containerName string
	path          string
	endpoint      string

	accountName             string
	accountKey              string
	connectionString        string
	noAuth                  bool
	useManagedIdentity      bool
	managedIdentityClientID string

	client *azblob.Client
}

func NewExporter(ctx context.Context, _ *connectors.Options, proto string, params map[string]string) (exporter.Exporter, error) {
	container, prefix, endpoint, accountName, accountKey, connectionString, noAuth, useManagedIdentity, managedIdentityClientID, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	exp := &azblobExporter{
		containerName:           container,
		path:                    prefix,
		endpoint:                endpoint,
		accountName:             accountName,
		accountKey:              accountKey,
		connectionString:        connectionString,
		noAuth:                  noAuth,
		useManagedIdentity:      useManagedIdentity,
		managedIdentityClientID: managedIdentityClientID,
	}

	if err := exp.connect(ctx); err != nil {
		return nil, err
	}

	return exp, nil
}

func (g *azblobExporter) connect(ctx context.Context) error {
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

	case g.useManagedIdentity:
		serviceURL := g.endpoint
		if serviceURL == "" {
			if g.accountName == "" {
				return fmt.Errorf("managed identity requires endpoint or account_name")
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
		return fmt.Errorf("missing credentials: provide connection_string, or account_name/account_key, or no_auth=true with endpoint, or use_managed_identity=true with endpoint or account_name")
	}
}

func (g *azblobExporter) Origin() string {
	if g.endpoint != "" {
		return g.endpoint + "/" + g.containerName
	}
	if g.accountName != "" {
		return fmt.Sprintf("%s.blob.core.windows.net/%s", g.accountName, g.containerName)
	}
	return g.containerName
}

func (g *azblobExporter) Type() string          { return "azblob" }
func (g *azblobExporter) Root() string          { return g.path }
func (g *azblobExporter) Flags() location.Flags { return 0 }

func (g *azblobExporter) Ping(ctx context.Context) error {
	pager := g.client.NewListBlobsFlatPager(g.containerName, &azblob.ListBlobsFlatOptions{
		MaxResults: toPtr(int32(1)),
	})
	if pager.More() {
		_, err := pager.NextPage(ctx)
		return err
	}
	return nil
}

func (g *azblobExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		blobName := path.Join(g.path, strings.TrimPrefix(record.Pathname, "/"))
		_, err := g.client.UploadStream(ctx, g.containerName, blobName, record.Reader, nil)
		results <- record.Error(err)
	}

	return nil
}

func (g *azblobExporter) Close(ctx context.Context) error {
	return nil
}
