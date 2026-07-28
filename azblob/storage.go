package azblob

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

func init() {
	storage.Register("azblob", 0, NewStore)
}

type azblobStore struct {
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

func parse(params map[string]string, proto string) (container, prefix, endpoint, accountName, accountKey, connectionString string, noAuth, useManagedIdentity bool, managedIdentityClientID string, err error) {
	for k, v := range params {
		switch k {
		case "connection_string":
			connectionString = v

		case "account_name":
			accountName = v

		case "account_key":
			accountKey = v

		case "endpoint":
			// Example:
			//   https://myaccount.blob.core.windows.net
			// or a custom endpoint / emulator endpoint
			endpoint = strings.TrimRight(v, "/")

		case "no_auth":
			noAuth, err = strconv.ParseBool(v)
			if err != nil {
				return "", "", "", "", "", "", false, false, "", fmt.Errorf("unknown value for no_auth %q: %w", v, err)
			}

		case "use_managed_identity":
			useManagedIdentity, err = strconv.ParseBool(v)
			if err != nil {
				return "", "", "", "", "", "", false, false, "", fmt.Errorf("unknown value for use_managed_identity %q: %w", v, err)
			}

		case "managed_identity_client_id":
			managedIdentityClientID = v

		case "location":
			// azblob://container/prefix
			container, prefix, _ = strings.Cut(strings.TrimPrefix(v, proto+"://"), "/")
			prefix = strings.Trim(prefix, "/")

		default:
			return "", "", "", "", "", "", false, false, "", fmt.Errorf("unknown option: %s", k)
		}
	}

	if container == "" {
		return "", "", "", "", "", "", false, false, "", fmt.Errorf("missing container in location")
	}

	return
}

func NewStore(ctx context.Context, proto string, params map[string]string) (storage.Store, error) {
	container, prefix, endpoint, accountName, accountKey, connectionString, noAuth, useManagedIdentity, managedIdentityClientID, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	return &azblobStore{
		containerName:           container,
		path:                    prefix,
		endpoint:                endpoint,
		accountName:             accountName,
		accountKey:              accountKey,
		connectionString:        connectionString,
		noAuth:                  noAuth,
		useManagedIdentity:      useManagedIdentity,
		managedIdentityClientID: managedIdentityClientID,
	}, nil
}

func (g *azblobStore) connect(ctx context.Context) error {
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
		client, err := azblob.NewClientWithNoCredential(g.endpoint+"/", nil)
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

		client, err := azblob.NewClientWithSharedKeyCredential(serviceURL+"/", cred, nil)
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

func (g *azblobStore) realpath(rel string) string {
	if g.path == "" {
		return rel
	}
	return path.Join(g.path, rel)
}

func (g *azblobStore) Create(ctx context.Context, config []byte) error {
	if err := g.connect(ctx); err != nil {
		return err
	}

	// Ensure the container exists.
	_, err := g.client.CreateContainer(ctx, g.containerName, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
		return err
	}

	// Repository existence is determined by CONFIG.
	cfgName := g.realpath("CONFIG")
	rd, err := g.client.DownloadStream(ctx, g.containerName, cfgName, nil)
	if err == nil {
		_ = rd.Body.Close()
		return fmt.Errorf("repository already exists")
	}
	if !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return err
	}

	_, err = g.client.UploadStream(ctx, g.containerName, cfgName, strings.NewReader(string(config)), nil)
	return err
}

func (g *azblobStore) Open(ctx context.Context) ([]byte, error) {
	if err := g.connect(ctx); err != nil {
		return nil, err
	}

	rd, err := g.client.DownloadStream(ctx, g.containerName, g.realpath("CONFIG"), nil)
	if err != nil {
		return nil, err
	}
	defer rd.Body.Close()

	data, err := io.ReadAll(rd.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (g *azblobStore) Ping(ctx context.Context) error {
	if err := g.connect(ctx); err != nil {
		return err
	}

	pager := g.client.NewListContainersPager(nil)
	if pager.More() {
		_, err := pager.NextPage(ctx)
		return err
	}
	return nil
}

func (g *azblobStore) Origin() string {
	if g.endpoint != "" {
		endpoint := strings.Replace(g.endpoint, "https://", "", 1)
		endpoint = strings.TrimRight(endpoint, "/")
		return endpoint + "/" + g.containerName
	}
	if g.accountName != "" {
		return fmt.Sprintf("%s.blob.core.windows.net/%s", g.accountName, g.containerName)
	}
	return g.containerName
}

func (g *azblobStore) Type() string          { return "azblob" }
func (g *azblobStore) Root() string          { return g.path }
func (g *azblobStore) Flags() location.Flags { return 0 }

func (g *azblobStore) Mode(ctx context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (g *azblobStore) Size(ctx context.Context) (int64, error) { return -1, nil }

func res2prefix(res storage.StorageResource) (string, error) {
	switch res {
	case storage.StorageResourceState:
		return "states", nil
	case storage.StorageResourcePackfile:
		return "packfiles", nil
	case storage.StorageResourceLock:
		return "locks", nil
	default:
		return "", fmt.Errorf("%w on %s", errors.ErrUnsupported, res)
	}
}

func (g *azblobStore) List(ctx context.Context, res storage.StorageResource) (ret []objects.MAC, err error) {
	prefix, err := res2prefix(res)
	if err != nil {
		return nil, err
	}

	prefix = g.realpath(prefix)
	l := len(prefix) + 4 // /%02x/

	pager := g.client.NewListBlobsFlatPager(g.containerName, &azblob.ListBlobsFlatOptions{
		Prefix: &prefix,
	})

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
			if len(name) <= l {
				continue
			}

			t, err := hex.DecodeString(name[l:])
			if err != nil {
				return nil, fmt.Errorf("decode %s key: %w", prefix, err)
			}
			if len(t) != 32 {
				return nil, fmt.Errorf("invalid %s name: %s", prefix, name)
			}
			ret = append(ret, objects.MAC(t))
		}
	}

	return ret, nil
}

func (g *azblobStore) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	prefix, err := res2prefix(res)
	if err != nil {
		return -1, err
	}

	name := fmt.Sprintf("%s/%02x/%016x", prefix, mac[0], mac)

	// UploadStream doesn't give you the copied byte count directly,
	// so count locally while streaming.
	cr := &countingReader{r: rd}
	_, err = g.client.UploadStream(ctx, g.containerName, g.realpath(name), cr, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to write %s object: %w", res, err)
	}

	return cr.n, nil
}

func (g *azblobStore) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, r *storage.Range) (io.ReadCloser, error) {
	prefix, err := res2prefix(res)
	if err != nil {
		return nil, err
	}

	name := fmt.Sprintf("%s/%02x/%016x", prefix, mac[0], mac)

	var opts *azblob.DownloadStreamOptions
	if r != nil {
		opts = &azblob.DownloadStreamOptions{
			Range: blob.HTTPRange{
				Offset: int64(r.Offset),
				Count:  int64(r.Length),
			},
		}
	}

	resp, err := g.client.DownloadStream(ctx, g.containerName, g.realpath(name), opts)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (g *azblobStore) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	prefix, err := res2prefix(res)
	if err != nil {
		return err
	}

	name := fmt.Sprintf("%s/%02x/%016x", prefix, mac[0], mac)
	_, err = g.client.DeleteBlob(ctx, g.containerName, g.realpath(name), nil)
	return err
}

func (g *azblobStore) Close(ctx context.Context) error {
	// azblob.Client does not need explicit closing.
	return nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}
