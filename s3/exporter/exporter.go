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

package exporter

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/sync/errgroup"
)

type S3Exporter struct {
	opts        *connectors.Options
	minioClient *minio.Client
	rootDir     string
	host        string
	bucket      string
	restoreDir  string
}

func init() {
	exporter.Register("s3", 0, NewS3Exporter)
}

func connect(endpoint string, useSsl, insecure bool, accessKeyID, secretAccessKey string) (*minio.Client, error) {
	transport, err := minio.DefaultTransport(useSsl)
	if err != nil {
		return nil, err
	}

	if useSsl && insecure {
		// NOTE: Minio will only initialize the TLSClientConfig pointer if useSsl
		// is true when creating a [minio.DefaultTransport]
		transport.TLSClientConfig.InsecureSkipVerify = true
	}

	// Initialize minio client object.
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

func NewS3Exporter(ctx context.Context, opts *connectors.Options, name string, config map[string]string) (exporter.Exporter, error) {
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

	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	var bucket, restoreDir, host string
	if virtualHost {
		bucket, host, _ = strings.Cut(parsed.Host, ".")
		restoreDir = strings.TrimPrefix(parsed.Path, "/")
	} else {
		bucket, restoreDir, _ = strings.Cut(parsed.RequestURI()[1:], "/")
		host = parsed.Host
	}

	conn, err := connect(host, useSsl, insecure, accessKey, secretAccessKey)
	if err != nil {
		return nil, err
	}

	err = conn.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code != "BucketAlreadyOwnedByYou" {
			return nil, fmt.Errorf("failed to create bucket %s: %w", bucket, err)
		}
	}

	return &S3Exporter{
		opts:        opts,
		rootDir:     restoreDir,
		minioClient: conn,
		host:        host,
		bucket:      bucket,
		restoreDir:  restoreDir,
	}, nil
}

func (p *S3Exporter) Root() string          { return p.restoreDir }
func (p *S3Exporter) Origin() string        { return p.host + "/" + p.bucket }
func (p *S3Exporter) Type() string          { return "s3" }
func (p *S3Exporter) Flags() location.Flags { return 0 }

func (p *S3Exporter) Ping(ctx context.Context) error {
	ok, err := p.minioClient.BucketExists(ctx, p.bucket)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("bucket does not exist")
	}
	return nil
}

func (p *S3Exporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(p.opts.MaxConcurrency)

	for record := range records {
		if record.Err != nil || record.IsXattr || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		g.Go(func() error {
			objname := strings.TrimLeft(path.Join(p.restoreDir, record.Pathname), "/")
			_, err := p.minioClient.PutObject(ctx, p.bucket, objname,
				record.Reader, record.FileInfo.Lsize, minio.PutObjectOptions{})
			results <- record.Error(err)
			return nil
		})
	}

	return g.Wait()
}

func (p *S3Exporter) Close(ctx context.Context) error {
	return nil
}
