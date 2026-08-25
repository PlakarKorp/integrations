// Package connector containes the openWRT plakar importer/exporter integrations.
package connector

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/integrations/openwrt/openwrtclient"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

type openwrt struct {
	login           string
	password        string
	host            string
	baseURL         string
	applySysupgrade bool
	rebootDevice    bool
	flags           location.Flags
	bClient         openwrtclient.OpenWRTClient
}

var (
	importerFlags = location.FLAG_NEEDACK | location.FLAG_STREAM
	exporterFlags = location.FLAG_NEEDACK
)

func (o *openwrt) Root() string { return "/" }
func (o *openwrt) Type() string { return "OpenWRT" }
func (o *openwrt) Flags() location.Flags {
	return o.flags
}
func (o *openwrt) Close(ctx context.Context) error { return nil }

func (o *openwrt) Ping(ctx context.Context) error {
	return o.bClient.Ping(ctx)
}

func init() {
	_ = importer.Register("openwrt", importerFlags, NewImporter)
	_ = exporter.Register("openwrt", exporterFlags, NewExporter)
}

func (o *openwrt) Origin() string {
	return o.host
}

// Come straight from the Tar integration
func finfo(hdr *tar.Header) objects.FileInfo {
	f := objects.FileInfo{
		Lname:      path.Base(hdr.Name),
		Lsize:      hdr.Size,
		Lmode:      hdr.FileInfo().Mode(),
		LmodTime:   hdr.ModTime,
		Ldev:       0, // XXX could use hdr.Devminor / hdr.Devmajor
		Luid:       uint64(hdr.Uid),
		Lgid:       uint64(hdr.Gid),
		Lnlink:     1,
		Lusername:  hdr.Uname,
		Lgroupname: hdr.Gname,
	}

	return f
}

func linkTarget(hdr *tar.Header) string {
	if hdr.Typeflag == tar.TypeSymlink {
		return hdr.Linkname
	}
	return ""
}

func (o *openwrt) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	bFile, err := o.bClient.GetBackupArchive(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = bFile.Close() }()

	g, err := gzip.NewReader(bFile)
	if err != nil {
		return err
	}
	defer func() { _ = g.Close() }()

	t := tar.NewReader(g)
	for {
		hdr, err := t.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		}

		fullpath := path.Join("/", hdr.Name)

		var record *connectors.Record
		if hdr.Typeflag == tar.TypeLink {
			record = connectors.NewError(fullpath, errors.New("hard links are not supported"))
		} else {
			record = connectors.NewRecord(fullpath, linkTarget(hdr), finfo(hdr), nil,
				func() (io.ReadCloser, error) {
					return io.NopCloser(t), nil
				})
		}

		select {
		case records <- record:
		case <-ctx.Done():
			return ctx.Err()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case ack, ok := <-results:
			if !ok {
				return errors.New("ack channel closed")
			}
			if ack.Err != nil {
				return fmt.Errorf("cannot ack %s %w", fullpath, ack.Err)
			}
			// Wait for ACK, cannot be async.
		}
	}
}

func (o *openwrt) handleExportChan(ctx context.Context, records <-chan *connectors.Record, result chan<- *connectors.Result) (*bytes.Buffer, error) {
	var archive bytes.Buffer

	gw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gw)

	closeHandles := func() error {
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gw.Close(); err != nil {
			return err
		}
		return nil
	}

	for {
		select {
		case record, ok := <-records:
			if !ok {
				return &archive, closeHandles()
			}

			if record.FileInfo.Lmode.IsDir() {
				result <- record.Ok()
				continue
			}

			th, err := tar.FileInfoHeader(record.FileInfo, record.Target)
			if err != nil {
				result <- record.Error(err)
				_ = closeHandles()
				return nil, err
			}
			// OpenWRT archive expect no leading /
			th.Name = strings.TrimPrefix(record.Pathname, "/")
			th.Uid = int(record.FileInfo.Luid)
			th.Gid = int(record.FileInfo.Lgid)
			th.Uname = record.FileInfo.Lusername
			th.Gname = record.FileInfo.Lgroupname

			if err := tw.WriteHeader(th); err != nil {
				result <- record.Error(err)
				return nil, err
			}

			if th.Typeflag == tar.TypeReg && record.Reader != nil {
				_, err := io.Copy(tw, record.Reader)
				if err != nil {
					result <- record.Error(err)
					_ = closeHandles()
					return nil, err
				}
			}

			result <- record.Ok()

		case <-ctx.Done():
			_ = closeHandles()
			return nil, ctx.Err()

		}
	}
}

func (o *openwrt) Export(ctx context.Context, records <-chan *connectors.Record, result chan<- *connectors.Result) error {
	defer close(result)

	archive, err := o.handleExportChan(ctx, records, result)
	if err != nil {
		return err
	}

	if err := o.bClient.UploadArchive(ctx, archive); err != nil {
		return fmt.Errorf("cannot upload archive %w", err)
	}
	if o.applySysupgrade {
		if err := o.bClient.Sysupgrade(ctx); err != nil {
			return fmt.Errorf("cannot sysupgrade %w", err)
		}
		if o.rebootDevice {
			if err := o.bClient.RebootDevice(ctx); err != nil {
				return fmt.Errorf("cannot reboot device %w", err)
			}
		}
	}
	return nil
}

func locationToURL(location string, useSSL bool) (string, string, error) {
	_, host, ok := strings.Cut(location, "://")
	if !ok {
		return "", "", errors.New("no scheme separator in location")
	}
	p := "http"
	if useSSL {
		p = p + "s"
	}
	return p + "://" + host, host, nil
}

func parseConfig(config map[string]string) (*openwrt, error) {
	var err error

	urlLocation, ok := config["location"]
	if !ok {
		return nil, errors.New("missing location")
	}

	login, ok := config["login"]
	if !ok {
		return nil, errors.New("missing login")
	}

	password := config["password"]

	useSSL := true
	strUseSSL, ok := config["use_ssl"]
	if ok {
		useSSL, err = strconv.ParseBool(strUseSSL)
		if err != nil {
			return nil, errors.New("invalid value for option use_ssl")
		}
	}

	applySysupgrade := true
	strApplySysupgrade, ok := config["apply_sysupgrade"]
	if ok {
		applySysupgrade, err = strconv.ParseBool(strApplySysupgrade)
		if err != nil {
			return nil, err
		}
	}

	rebootDevice := true
	strRebootDevice, ok := config["reboot_device"]
	if ok {
		rebootDevice, err = strconv.ParseBool(strRebootDevice)
		if err != nil {
			return nil, err
		}
	}

	baseURL, host, err := locationToURL(urlLocation, useSSL)
	if err != nil {
		return nil, err
	}

	timeout := 30
	strTimeout, ok := config["timeout"]
	if ok {
		timeout, err = strconv.Atoi(strTimeout)
		if err != nil {
			return nil, err
		}
		if timeout <= 0 {
			return nil, errors.New("invalid timeout value")
		}
	}

	insecureSkipVerify := false
	strInsecureSkipVerify, ok := config["insecure_skip_verify"]
	if ok {
		insecureSkipVerify, err = strconv.ParseBool(strInsecureSkipVerify)
		if err != nil {
			return nil, err
		}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: insecureSkipVerify,
	}
	transport.ResponseHeaderTimeout = time.Duration(timeout) * time.Second
	httpClient := &http.Client{Transport: transport}

	return &openwrt{
		login:           login,
		password:        password,
		baseURL:         baseURL,
		host:            host,
		applySysupgrade: applySysupgrade,
		rebootDevice:    rebootDevice,
		bClient:         openwrtclient.NewBackupClient(baseURL, login, password, openwrtclient.WithHTTPClient(httpClient)),
	}, nil
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	o, err := parseConfig(config)
	if err != nil {
		return nil, err
	}
	o.flags = importerFlags
	return o, nil
}

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	o, err := parseConfig(config)
	if err != nil {
		return nil, err
	}
	o.flags = exporterFlags
	return o, nil
}
