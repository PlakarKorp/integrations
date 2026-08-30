package etcd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"strconv"
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

	// The bare etcd:// scheme used to be rewritten to http://, so the
	// username and password below travelled in cleartext by default with
	// nothing saying so.  It maps to https:// now; cleartext is etcd+http://,
	// or etcd:// with insecure=true.
	insecure := false
	if v, ok := config["insecure"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid insecure value %q", v)
		}
		insecure = b
	}

	switch proto {
	case "etcd":
		scheme := "https"
		if insecure {
			scheme = "http"
		}
		location = scheme + strings.TrimPrefix(location, proto)
	case "etcd+http", "etcd+https":
		location = strings.TrimPrefix(location, "etcd+")
	}

	// extract the "hostname" from location, needed for Origin(),
	// i.e. metadata.
	//
	// This used to index with len(proto)+3, an offset computed against the
	// original scheme after location had already been rewritten to a
	// different one: etcd+https://host:2379 yielded an origin of "2379", and
	// a short enough host panicked with a slice bounds error.
	origin := location
	if _, rest, found := strings.Cut(origin, "://"); found {
		origin = rest
	}
	origin, _, _ = strings.Cut(origin, "/")

	endpoints := []string{location}
	if es, ok := config["endpoints"]; ok {
		endpoints = strings.Split(es, ",")
	}

	cfg := clientv3.Config{
		Endpoints:   endpoints,
		Username:    config["username"],
		Password:    config["password"],
		DialTimeout: 10 * time.Second,
	}

	tlsConfig, err := etcdTLS(config)
	if err != nil {
		return nil, err
	}
	cfg.TLS = tlsConfig

	client, err := clientv3.New(cfg)
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

// etcdTLS builds the client TLS configuration.  Returning nil leaves the
// clientv3 default, which uses the system roots for an https endpoint.
func etcdTLS(config map[string]string) (*tls.Config, error) {
	var (
		caFile     = config["ca_file"]
		certFile   = config["cert_file"]
		keyFile    = config["key_file"]
		noVerifyOK = config["tls_insecure_no_verify"]
	)

	noVerify := false
	if noVerifyOK != "" {
		b, err := strconv.ParseBool(noVerifyOK)
		if err != nil {
			return nil, fmt.Errorf("invalid tls_insecure_no_verify value %q", noVerifyOK)
		}
		noVerify = b
	}

	if caFile == "" && certFile == "" && keyFile == "" && !noVerify {
		return nil, nil
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: noVerify, //nolint:gosec // opt-in
	}

	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s contains no certificate", caFile)
		}
		cfg.RootCAs = pool
	}

	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("cert_file and key_file must be given together")
	}
	if certFile != "" {
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("loading client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}

	return cfg, nil
}
