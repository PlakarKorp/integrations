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
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"

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
	ssec    encrypt.ServerSide
}

func init() {
	importer.Register("s3", 0, NewS3Importer)
}

func connect(endpoint string, useSsl, insecure bool, accessKeyID, secretAccessKey string) (*minio.Client, error) {
	transport, err := minio.DefaultTransport(useSsl)
	if err != nil {
		return nil, err
	}

	if useSsl && insecure {
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

	virtualHost := false
	if value, ok := config["virtual_host"]; ok {
		tmp, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid virtual_host value")
		}
		virtualHost = tmp
	}

	var ssec encrypt.ServerSide
	if value, ok := config["sse_customer_key"]; ok {
		keyBytes, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("invalid sse_customer_key: must be base64-encoded: %w", err)
		}
		ssec, err = encrypt.NewSSEC(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("invalid sse_customer_key: %w", err)
		}
	}

	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	var bucket, scanDir, host string
	if virtualHost {
		bucket, host, _ = strings.Cut(parsed.Host, ".")
		scanDir = strings.TrimPrefix(parsed.Path, "/")
		if host == "" {
			return nil, fmt.Errorf("failed to extract bucket name from URL (maybe virtual_host needs to be disable?)")
		}
	} else {
		bucket, scanDir, _ = strings.Cut(parsed.RequestURI()[1:], "/")
		host = parsed.Host
	}

	if bucket == "" || host == "" {
		return nil, fmt.Errorf("failed to parse the location: bucket name or host name are empty")
	}

	if !strings.HasPrefix(scanDir, "/") {
		scanDir = "/" + scanDir
	}

	conn, err := connect(host, useSsl, insecure, accessKey, secretAccessKey)
	if err != nil {
		return nil, err
	}

	return &S3Importer{
		bucket:      bucket,
		scanDir:     scanDir,
		minioClient: conn,
		host:        host,
		ssec:        ssec,
	}, nil
}

func (p *S3Importer) Root() string          { return p.scanDir }
func (p *S3Importer) Origin() string        { return path.Join(p.host, p.bucket) }
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

	listopts := minio.ListObjectsOptions{
		Prefix:    strings.TrimPrefix(p.scanDir, "/"),
		Recursive: true,
	}
	var err error
	for object := range p.minioClient.ListObjects(ctx, p.bucket, listopts) {
		if object.Err != nil {
			err = object.Err
			continue // per documentation, we have to drain the channel
		}

		// Some backend actually return _folders_, which they
		// shouldn't so just skip over those.
		if strings.HasSuffix(object.Key, "/") {
			continue
		}

		fi := objects.FileInfo{
			Lname:    path.Base("/" + object.Key),
			Lsize:    object.Size,
			Lmode:    0o700,
			LmodTime: object.LastModified,
			Ldev:     1,
		}

		records <- connectors.NewRecord("/"+object.Key, "", fi, nil, func() (io.ReadCloser, error) {
			return p.minioClient.GetObject(ctx, p.bucket, object.Key, minio.GetObjectOptions{ServerSideEncryption: p.ssec})
		})
	}

	return err
}

func (p *S3Importer) Close(ctx context.Context) error {
	return nil
}
