package gcs

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	gstorage "cloud.google.com/go/storage"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

func init() {
	storage.Register("gs", 0, NewStore)
}

type gcsStore struct {
	bucketName string
	path       string
	opts       []option.ClientOption

	client *gstorage.Client
	bucket *gstorage.BucketHandle
}

func parse(params map[string]string, proto string) (string, string, []option.ClientOption, error) {
	var (
		opts   []option.ClientOption
		bucket string
		path   string
	)

	for k, v := range params {
		switch k {
		case "credentials_file":
			opts = append(opts, option.WithCredentialsFile(v))

		case "credentials_json":
			opts = append(opts, option.WithCredentialsJSON([]byte(v)))

		case "endpoint":
			opts = append(opts, option.WithEndpoint(v))

		case "no_auth":
			noauth, err := strconv.ParseBool(v)
			if err != nil {
				err = fmt.Errorf("unknown value for no_auth %q: %w", v, err)
				return "", "", nil, err
			}
			if noauth {
				opts = append(opts, option.WithoutAuthentication())
			}

		case "location":
			bucket, path, _ = strings.Cut(strings.TrimPrefix(v, proto+"://"), "/")
			path = strings.Trim(path, "/")

		default:
			return "", "", nil, fmt.Errorf("unknown option: %s", k)
		}
	}

	// telemetry should be opt-in?
	opts = append(opts, option.WithTelemetryDisabled())

	return bucket, path, opts, nil
}

func NewStore(ctx context.Context, proto string, params map[string]string) (storage.Store, error) {
	bucket, path, opts, err := parse(params, proto)
	if err != nil {
		return nil, err
	}

	return &gcsStore{
		bucketName: bucket,
		path:       path,
		opts:       opts,
	}, nil
}

func (g *gcsStore) connect(ctx context.Context) error {
	if g.client != nil {
		return nil
	}

	client, err := gstorage.NewClient(ctx, g.opts...)
	if err != nil {
		return err
	}

	g.client = client
	g.bucket = client.Bucket(g.bucketName)
	return nil
}

func (g *gcsStore) realpath(rel string) string { return path.Join(g.path, rel) }

func (g *gcsStore) Create(ctx context.Context, config []byte) error {
	if err := g.connect(ctx); err != nil {
		return err
	}

	obj := g.bucket.Object(g.realpath("CONFIG"))
	_, err := obj.Attrs(ctx)
	if errors.Is(err, gstorage.ErrObjectNotExist) {
		w := obj.NewWriter(ctx)
		defer w.Close()
		_, err = w.Write(config)
		return err
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("repository already exists")
}

func (g *gcsStore) Open(ctx context.Context) ([]byte, error) {
	if err := g.connect(ctx); err != nil {
		return nil, err
	}

	rd, err := g.bucket.Object(g.realpath("CONFIG")).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer rd.Close()

	data, err := io.ReadAll(rd)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (g *gcsStore) Ping(ctx context.Context) error {
	if err := g.connect(ctx); err != nil {
		return err
	}

	_, err := g.client.Bucket(g.bucketName).Attrs(ctx)
	return err
}

func (g *gcsStore) Origin() string        { return "" }
func (g *gcsStore) Type() string          { return "gs" }
func (g *gcsStore) Root() string          { return g.path }
func (g *gcsStore) Flags() location.Flags { return 0 }

func (g *gcsStore) Mode(ctx context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (g *gcsStore) Size(ctx context.Context) (int64, error) { return -1, nil }

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

func (g *gcsStore) List(ctx context.Context, res storage.StorageResource) (ret []objects.MAC, err error) {
	prefix, err := res2prefix(res)
	if err != nil {
		return nil, err
	}

	prefix = g.realpath(prefix)
	l := len(prefix) + 4 // /%02x/

	query := &gstorage.Query{Prefix: prefix}
	it := g.bucket.Objects(ctx, query)
	for {
		obj, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}

		if len(obj.Name) > l {
			t, err := hex.DecodeString(obj.Name[l:])
			if err != nil {
				return nil, fmt.Errorf("decode %s key: %w", prefix, err)
			}
			if len(t) != 32 {
				return nil, fmt.Errorf("invalid %s name: %s",
					prefix, obj.Name)
			}
			ret = append(ret, objects.MAC(t))
		}
	}
	return
}

func (g *gcsStore) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	prefix, err := res2prefix(res)
	if err != nil {
		return -1, err
	}

	name := fmt.Sprintf("%s/%02x/%016x", prefix, mac[0], mac)
	w := g.bucket.Object(g.realpath(name)).NewWriter(ctx)

	len, err := io.Copy(w, rd)
	if err != nil {
		w.Close()
		return 0, fmt.Errorf("failed to write %s object: %w", res, err)
	}

	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("failed to write %s object: %w", res, err)
	}

	return len, nil
}

func (g *gcsStore) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, r *storage.Range) (io.ReadCloser, error) {
	prefix, err := res2prefix(res)
	if err != nil {
		return nil, err
	}

	var (
		name = fmt.Sprintf("%s/%02x/%016x", prefix, mac[0], mac)
		obj  = g.bucket.Object(g.realpath(name))
	)

	if r != nil {
		return obj.NewRangeReader(ctx, int64(r.Offset), int64(r.Length))
	}
	return obj.NewReader(ctx)
}

func (g *gcsStore) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	prefix, err := res2prefix(res)
	if err != nil {
		return err
	}

	name := fmt.Sprintf("%s/%02x/%016x", prefix, mac[0], mac)
	return g.bucket.Object(g.realpath(name)).Delete(ctx)
}

func (g *gcsStore) Close(ctx context.Context) error {
	if g.client != nil {
		return g.client.Close()
	}
	return nil
}
