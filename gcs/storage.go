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
	ctx        context.Context
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
		ctx:        ctx,
		bucketName: bucket,
		path:       path,
		opts:       opts,
	}, nil
}

func (g *gcsStore) connect() error {
	client, err := storage.NewClient(g.ctx, g.opts...)
	if err != nil {
		return err
	}

	g.client = client
	g.bucket = client.Bucket(g.bucketName)
	return nil
}

func (g *gcsStore) realpath(rel string) string { return path.Join(g.path, rel) }

func (g *gcsStore) put(prefix string, mac objects.MAC, rd io.Reader) (int64, error) {
	name := fmt.Sprintf("%s/%02x/%016x", prefix, mac[0], mac)
	w := g.bucket.Object(g.realpath(name)).NewWriter(g.ctx)

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

func (g *gcsStore) getall(prefix string) (ret []objects.MAC, err error) {
	prefix = g.realpath(prefix)
	l := len(prefix) + 4 // /%02x/

	query := &storage.Query{Prefix: prefix}
	it := g.bucket.Objects(g.ctx, query)
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
	if err := g.connect(); err != nil {
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
	if err := g.connect(); err != nil {
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

func (g *gcsStore) Location() string    { return "gcs://" + path.Join(g.bucketName, g.path) }
func (g *gcsStore) Mode() kstorage.Mode { return kstorage.ModeRead | kstorage.ModeWrite }
func (g *gcsStore) Size() int64         { return -1 }

func (g *gcsStore) GetStates() ([]objects.MAC, error) {
	return g.getall("states")
}

func (g *gcsStore) PutState(mac objects.MAC, rd io.Reader) (int64, error) {
	return g.put("locks", mac, rd)
}

func (g *gcsStore) GetState(mac objects.MAC) (io.ReadCloser, error) {
	name := fmt.Sprintf("states/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).NewReader(g.ctx)
}

func (g *gcsStore) DeleteState(mac objects.MAC) error {
	name := fmt.Sprintf("states/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).Delete(g.ctx)
}

func (g *gcsStore) GetPackfiles() ([]objects.MAC, error) {
	return g.getall("packfiles")
}

func (g *gcsStore) PutPackfile(mac objects.MAC, rd io.Reader) (int64, error) {
	return g.put("packfiles", mac, rd)
}

func (g *gcsStore) GetPackfile(mac objects.MAC) (io.ReadCloser, error) {
	name := fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).NewReader(g.ctx)
}

func (g *gcsStore) GetPackfileBlob(mac objects.MAC, offset uint64, length uint32) (io.ReadCloser, error) {
	name := fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).NewRangeReader(g.ctx, int64(offset), int64(length))
}

func (g *gcsStore) DeletePackfile(mac objects.MAC) error {
	name := fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac)
	return g.bucket.Object(g.realpath(name)).Delete(g.ctx)
}

func (g *gcsStore) GetLocks() ([]objects.MAC, error) {
	return g.getall("locks")
}

func (g *gcsStore) PutLock(lockID objects.MAC, rd io.Reader) (int64, error) {
	return g.put("locks", lockID, rd)
}

func (g *gcsStore) GetLock(lockID objects.MAC) (io.ReadCloser, error) {
	name := fmt.Sprintf("locks/%02x/%016x", lockID[0], lockID)
	return g.bucket.Object(g.realpath(name)).NewReader(g.ctx)
}

func (g *gcsStore) DeleteLock(lockID objects.MAC) error {
	name := fmt.Sprintf("locks/%02x/%016x", lockID[0], lockID)
	return g.bucket.Object(g.realpath(name)).Delete(g.ctx)
}

func (g *gcsStore) Close() error {
	return g.client.Close()
}
