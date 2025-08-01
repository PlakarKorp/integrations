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

	"cloud.google.com/go/storage"
	"github.com/PlakarKorp/kloset/objects"
	kstorage "github.com/PlakarKorp/kloset/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type gcsStore struct {
	bucketName string
	path       string
	opts       []option.ClientOption

	client *storage.Client
	bucket *storage.BucketHandle
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
			bucket, path, _ = strings.Cut(strings.TrimPrefix(k, proto+"://"), "/")
			path = strings.Trim(path, "/")

		default:
			return "", "", nil, fmt.Errorf("unknown option: %s", k)
		}
	}

	// telemetry should be opt-in?
	opts = append(opts, option.WithTelemetryDisabled())

	return bucket, path, opts, nil
}

func NewStore(ctx context.Context, proto string, params map[string]string) (kstorage.Store, error) {
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
	client, err := storage.NewClient(ctx, g.opts...)
	if err != nil {
		return err
	}

	g.client = client
	g.bucket = client.Bucket(g.bucketName)
	return nil
}

func (g *gcsStore) realpath(rel string) string { return path.Join(g.path, rel) }

func (g *gcsStore) put(ctx context.Context, prefix string, mac objects.MAC, rd io.Reader) (int64, error) {
	name := fmt.Sprintf("%s/%02x/%016x", prefix, mac[0], mac)
	w := g.bucket.Object(g.realpath(name)).NewWriter(ctx)

	len, err := io.Copy(w, rd)
	if err != nil {
		w.Close()
		return 0, fmt.Errorf("failed to write object: %w", err)
	}

	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("failed to write object: %w", err)
	}

	return len, nil
}

func (g *gcsStore) getall(ctx context.Context, prefix string) (ret []objects.MAC, err error) {
	prefix = g.realpath(prefix)
	l := len(prefix) + 4 // /%02x/

	query := &storage.Query{Prefix: prefix}
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

func (g *gcsStore) Create(ctx context.Context, config []byte) error {
	if err := g.connect(ctx); err != nil {
		return err
	}

	obj := g.bucket.Object(g.realpath("CONFIG"))
	_, err := obj.Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
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

func (g *gcsStore) Location(ctx context.Context) (string, error) {
	return "gcs://" + path.Join(g.bucketName, g.path), nil
}

func (g *gcsStore) Mode(ctx context.Context) (kstorage.Mode, error) {
	return kstorage.ModeRead | kstorage.ModeWrite, nil
}

func (g *gcsStore) Size(ctx context.Context) (int64, error) { return -1, nil }

func (g *gcsStore) GetStates(ctx context.Context) ([]objects.MAC, error) {
	return g.getall(ctx, "states")
}

func (g *gcsStore) PutState(ctx context.Context, mac objects.MAC, rd io.Reader) (int64, error) {
	return g.put(ctx, "locks", mac, rd)
}

func (g *gcsStore) GetState(ctx context.Context, mac objects.MAC) (io.ReadCloser, error) {
	name := fmt.Sprintf("states/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).NewReader(ctx)
}

func (g *gcsStore) DeleteState(ctx context.Context, mac objects.MAC) error {
	name := fmt.Sprintf("states/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).Delete(ctx)
}

func (g *gcsStore) GetPackfiles(ctx context.Context) ([]objects.MAC, error) {
	return g.getall(ctx, "packfiles")
}

func (g *gcsStore) PutPackfile(ctx context.Context, mac objects.MAC, rd io.Reader) (int64, error) {
	return g.put(ctx, "packfiles", mac, rd)
}

func (g *gcsStore) GetPackfile(ctx context.Context, mac objects.MAC) (io.ReadCloser, error) {
	name := fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).NewReader(ctx)
}

func (g *gcsStore) GetPackfileBlob(ctx context.Context, mac objects.MAC, offset uint64, length uint32) (io.ReadCloser, error) {
	name := fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).NewRangeReader(ctx, int64(offset), int64(length))
}

func (g *gcsStore) DeletePackfile(ctx context.Context, mac objects.MAC) error {
	name := fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).Delete(ctx)
}

func (g *gcsStore) GetLocks(ctx context.Context) ([]objects.MAC, error) {
	return g.getall(ctx, "locks")
}

func (g *gcsStore) PutLock(ctx context.Context, lockID objects.MAC, rd io.Reader) (int64, error) {
	return g.put(ctx, "locks", lockID, rd)
}

func (g *gcsStore) GetLock(ctx context.Context, lockID objects.MAC) (io.ReadCloser, error) {
	name := fmt.Sprintf("locks/%02x/%016x", lockID[0], lockID)
	return g.bucket.Object(g.realpath(name)).NewReader(ctx)
}

func (g *gcsStore) DeleteLock(ctx context.Context, lockID objects.MAC) error {
	name := fmt.Sprintf("locks/%02x/%016x", lockID[0], lockID)
	return g.bucket.Object(g.realpath(name)).Delete(ctx)
}

func (g *gcsStore) Close(ctx context.Context) error {
	return g.client.Close()
}
