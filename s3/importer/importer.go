/*
 * Copyright (c) 2023 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package importer

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

type S3Importer struct {
	minioClient *minio.Client

	bucket  string
	host    string
	scanDir string
}

func init() {
	importer.Register("s3", 0, NewS3Importer)
}

func connect(location *url.URL, useSsl, insecure bool, accessKeyID, secretAccessKey string) (*minio.Client, error) {
	endpoint := location.Host

	transport, err := minio.DefaultTransport(useSsl)
	if err != nil {
		return nil, err
	}

	if insecure {
		transport.TLSClientConfig.InsecureSkipVerify = true
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure:    useSsl,
		Transport: transport,
	})
	if err != nil {
		return nil, err
	}

	client.SetAppInfo("plakar", "v1.1.0")

	return client, nil
}

func NewS3Importer(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (importer.Importer, error) {
	target := config["location"]

	var accessKey string
	if tmp, ok := config["access_key"]; !ok {
		return nil, fmt.Errorf("missing access_key")
	} else {
		accessKey = tmp
	}

	var secretAccessKey string
	if tmp, ok := config["secret_access_key"]; !ok {
		return nil, fmt.Errorf("missing secret_access_key")
	} else {
		secretAccessKey = tmp
	}

	useSsl := true
	if value, ok := config["use_tls"]; ok {
		tmp, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid use_tls value")
		}
		useSsl = tmp
	}

	insecure := false
	if value, ok := config["tls_insecure_no_verify"]; ok {
		tmp, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid tls_insecure_no_verify value")
		}
		insecure = tmp
	}

	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	conn, err := connect(parsed, useSsl, insecure, accessKey, secretAccessKey)
	if err != nil {
		return nil, err
	}

	atoms := strings.Split(parsed.RequestURI()[1:], "/")
	bucket := atoms[0]
	scanDir := path.Clean("/" + strings.Join(atoms[1:], "/"))

	return &S3Importer{
		bucket:      bucket,
		scanDir:     scanDir,
		minioClient: conn,
		host:        parsed.Host,
	}, nil
}

func (p *S3Importer) Root() string          { return p.scanDir }
func (p *S3Importer) Origin() string        { return p.host + "/" + p.bucket }
func (p *S3Importer) Type() string          { return "s3" }
func (p *S3Importer) Flags() location.Flags { return 0 }

func (p *S3Importer) Ping(ctx context.Context) error {
	ok, err := p.minioClient.BucketExists(ctx, p.bucket)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("bucket does not exist")
	}
	return nil
}

func (p *S3Importer) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	// racy, but ListObjects doesn't seem to signal failure
	// accessing the APIs.
	if err := p.Ping(ctx); err != nil {
		return err
	}

	listopts := minio.ListObjectsOptions{
		Prefix:    strings.TrimPrefix(p.scanDir, "/"),
		Recursive: true,
	}
	for object := range p.minioClient.ListObjects(ctx, p.bucket, listopts) {
		// Some backend actually return _folders_, which they
		// shouldn't so just skip over those.
		if strings.HasSuffix(object.Key, "/") {
			continue
		}

		fi := objects.FileInfo{
			Lname:    path.Base("/" + object.Key),
			Lsize:    object.Size,
			Lmode:    0700,
			LmodTime: object.LastModified,
			Ldev:     1,
		}

		records <- connectors.NewRecord("/"+object.Key, "", fi, nil, func() (io.ReadCloser, error) {
			return p.minioClient.GetObject(ctx, p.bucket, object.Key, minio.GetObjectOptions{})
		})
	}

	return nil
}

func (p *S3Importer) Close(ctx context.Context) error {
	return nil
}
