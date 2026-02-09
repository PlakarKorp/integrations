package etcd

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	clientv3 "go.etcd.io/etcd/client/v3"
)

func init() {
	importer.Register("etcd", 0, NewImporter)
	importer.Register("etcd+http", 0, NewImporter)
	importer.Register("etcd+https", 0, NewImporter)
}

type etcd struct {
	client *clientv3.Client
	maint  clientv3.Maintenance
	origin string
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	location := config["location"]
	switch proto {
	case "etcd":
		location = "http" + location[len(proto):]
	case "etcd+http", "etcd+https":
		location = location[len("etcd+"):]
	}

	// extract the "hostname" from location, needed for Origin(),
	// i.e. metadata.
	origin := location[len(proto)+3:] // +3 for ://
	origin, _, _ = strings.Cut(origin, "/")

	endpoints := []string{location}
	if es, ok := config["endpoints"]; ok {
		endpoints = strings.Split(es, ",")
	}

	client, err := clientv3.New(clientv3.Config{
		Endpoints: endpoints,
		Username:  config["username"],
		Password:  config["password"],
	})
	if err != nil {
		return nil, err
	}

	return &etcd{
		client: client,
		maint:  clientv3.NewMaintenance(client),
		origin: origin,
	}, nil
}

func (e *etcd) Origin() string        { return e.origin }
func (e *etcd) Type() string          { return "etcd" }
func (e *etcd) Root() string          { return "/" }
func (e *etcd) Flags() location.Flags { return location.FLAG_NEEDACK }

func (e *etcd) Ping(ctx context.Context) error {
	// maybe we can even avoid this since NewImporter already does
	// the connection.
	_, err := e.maint.Status(ctx, "health")
	return err
}

func (e *etcd) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	dump := func() (io.ReadCloser, error) {
		return e.maint.Snapshot(ctx)
	}

	finfo := objects.FileInfo{
		Lname:    "dump",
		Lsize:    -1,
		Lmode:    0o644,
		LmodTime: time.Now(), // XXX
	}

	records <- connectors.NewRecord("/dump", "", finfo, nil, dump)
	res := <-results // wait for the ack
	return res.Err
}

func (e *etcd) Close(ctx context.Context) error {
	return e.client.Close()
}
