/*
 * Copyright (c) 2021 Gilles Chehade <gilles@poolp.org>
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

package storage

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/reading"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	minioClient  *minio.Client
	host         string
	bucket       string
	prefixDir    string
	storageClass string

	bufPool sync.Pool

	putObjectOptions minio.PutObjectOptions
}

func init() {
	storage.Register("s3", 0, NewStore)
}

func NewStore(ctx context.Context, proto string, storeConfig map[string]string) (storage.Store, error) {
	var accessKey string
	if value, ok := storeConfig["access_key"]; !ok {
		return nil, fmt.Errorf("missing access_key")
	} else {
		accessKey = value
	}

	var secretAccessKey string
	if value, ok := storeConfig["secret_access_key"]; !ok {
		return nil, fmt.Errorf("missing secret_access_key")
	} else {
		secretAccessKey = value
	}

	useSsl := true
	if value, ok := storeConfig["use_tls"]; ok {
		tmp, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid use_tls value")
		}
		useSsl = tmp
	}

	insecure := false
	if value, ok := storeConfig["tls_insecure_no_verify"]; ok {
		tmp, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("invalid tls_insecure_no_verify value")
		}
		insecure = tmp
	}

	storageClass := "STANDARD"
	if value, ok := storeConfig["storage_class"]; ok {
		storageClass = strings.ToUpper(value)
		if storageClass != "STANDARD" && storageClass != "REDUCED_REDUNDANCY" && storageClass != "STANDARD_IA" && storageClass != "ONEZONE_IA" && storageClass != "INTELLIGENT_TIERING" && storageClass != "GLACIER" && storageClass != "GLACIER_IR" && storageClass != "DEEP_ARCHIVE" {
			return nil, fmt.Errorf("invalid storage_class value")
		}
	}

	u, err := url.Parse(storeConfig["location"])
	if err != nil {
		return nil, fmt.Errorf("parse location: %w", err)
	}

	bucket, prefixDir, _ := strings.Cut(u.RequestURI()[1:], "/")
	if prefixDir != "" && !strings.HasSuffix(prefixDir, "/") {
		prefixDir += "/"
	}

	transport, err := minio.DefaultTransport(useSsl)
	if err != nil {
		return nil, fmt.Errorf("failed to create default transport: %w", err)
	}

	if insecure {
		transport.TLSClientConfig.InsecureSkipVerify = true
	}

	// Initialize minio client object.
	client, err := minio.New(u.Host, &minio.Options{
		Creds:     credentials.NewStaticV4(accessKey, secretAccessKey, ""),
		Secure:    useSsl,
		Transport: transport,
	})

	client.SetAppInfo("plakar", "v1.1.0")

	return &Store{
		minioClient:  client,
		host:         u.Host,
		bucket:       bucket,
		prefixDir:    prefixDir,
		storageClass: storageClass,

		bufPool: sync.Pool{
			New: func() any {
				return &bytes.Buffer{}
			},
		},

		putObjectOptions: minio.PutObjectOptions{
			// Some providers (eg. BlackBlaze) return the error
			// "Unsupported header 'x-amz-checksum-algorithm'" if SendContentMd5
			// is not set.
			StorageClass:   storageClass,
			SendContentMd5: true,
		},
	}, nil
}

func (s *Store) realpath(path string) string {
	return s.prefixDir + path
}

func (s *Store) Create(ctx context.Context, config []byte) error {
	exists, err := s.minioClient.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check if bucket exists: %w", err)
	}
	if !exists {
		err = s.minioClient.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{})
		if err != nil {
			return fmt.Errorf("make bucket: %w", err)
		}
	}

	_, err = s.minioClient.StatObject(ctx, s.bucket, s.realpath("CONFIG"), minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code != "NoSuchKey" {
			return fmt.Errorf("stat object CONFIG: %w", err)
		}
	} else {
		return fmt.Errorf("bucket already initialized")
	}

	if s.mode()&storage.ModeRead == 0 {
		_, err = s.minioClient.PutObject(ctx, s.bucket, s.realpath("CONFIG.frozen"), bytes.NewReader(config), int64(len(config)), s.putObjectOptions)
		if err != nil {
			return fmt.Errorf("put object CONFIG.frozen: %w", err)
		}
	}

	putObjectOptions := s.putObjectOptions
	putObjectOptions.StorageClass = "STANDARD"

	_, err = s.minioClient.PutObject(ctx, s.bucket, s.realpath("CONFIG"), bytes.NewReader(config), int64(len(config)), putObjectOptions)
	if err != nil {
		return fmt.Errorf("put object CONFIG: %w", err)
	}

	return nil
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	exists, err := s.minioClient.BucketExists(ctx, s.bucket)
	if err != nil {
		return nil, fmt.Errorf("error checking if bucket exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("bucket does not exist")
	}

	object, err := s.minioClient.GetObject(ctx, s.bucket, s.realpath("CONFIG"), minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting object: %w", err)
	}

	data, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("error reading object: %w", err)
	}
	object.Close()

	return data, nil
}

func (p *Store) Ping(ctx context.Context) error {
	ok, err := p.minioClient.BucketExists(ctx, p.bucket)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("bucket does not exist")
	}
	return nil
}

func (s *Store) Origin() string        { return s.host }
func (s *Store) Root() string          { return path.Join("/", s.bucket, s.prefixDir) }
func (s *Store) Type() string          { return "s3" }
func (s *Store) Flags() location.Flags { return 0 }

func (s *Store) mode() storage.Mode {
	if s.storageClass == "GLACIER" || s.storageClass == "DEEP_ARCHIVE" {
		return storage.ModeWrite
	}
	return storage.ModeRead | storage.ModeWrite
}

func (s *Store) Mode(ctx context.Context) (storage.Mode, error) {
	return s.mode(), nil
}

func (s *Store) Size(ctx context.Context) (int64, error) {
	return -1, nil
}

func (s *Store) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	var prefix string
	var prefixSize int

	switch res {
	case storage.StorageResourcePackfile:
		prefix = s.realpath("packfiles/")
		prefixSize = len(prefix) + 3 // prefix + len(%02x/) encoded
	case storage.StorageResourceState:
		prefix = s.realpath("states/")
		prefixSize = len(prefix) + 3 // prefix + len(%02x/) encoded
	case storage.StorageResourceLock:
		prefix = s.realpath("locks/")
		prefixSize = len(prefix)
	default:
		return nil, errors.ErrUnsupported
	}

	ret := make([]objects.MAC, 0)
	for object := range s.minioClient.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if strings.HasPrefix(object.Key, prefix) && len(object.Key) >= prefixSize {
			t, err := hex.DecodeString(object.Key[prefixSize:])
			if err != nil {
				return nil, fmt.Errorf("decode %s key: %w", res, err)
			}
			if len(t) != 32 {
				continue
			}
			ret = append(ret, objects.MAC(t))
		}
	}
	return ret, nil

}

func (s *Store) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	switch res {
	case storage.StorageResourcePackfile:
		buf := s.bufPool.Get().(*bytes.Buffer)
		copied, err := io.Copy(buf, rd)
		if err != nil {
			return 0, fmt.Errorf("read %s object: %w", res, err)
		}

		info, err := s.minioClient.PutObject(ctx, s.bucket, s.realpath(fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac)), buf, copied, s.putObjectOptions)
		if err != nil {
			return 0, fmt.Errorf("put %s object: %w", res, err)
		}

		buf.Reset()
		s.bufPool.Put(buf)
		return info.Size, nil
	case storage.StorageResourceState:
		info, err := s.minioClient.PutObject(ctx, s.bucket, s.realpath(fmt.Sprintf("states/%02x/%016x", mac[0], mac)), rd, -1, s.putObjectOptions)
		if err != nil {
			return 0, fmt.Errorf("put %s object: %w", res, err)
		}

		return info.Size, nil
	case storage.StorageResourceLock:
		putObjectOptions := s.putObjectOptions
		putObjectOptions.StorageClass = "STANDARD"

		info, err := s.minioClient.PutObject(ctx, s.bucket, s.realpath(fmt.Sprintf("locks/%016x", mac)), rd, -1, putObjectOptions)
		if err != nil {
			return 0, fmt.Errorf("put %s object: %w", res, err)
		}
		return info.Size, nil
	}

	return -1, errors.ErrUnsupported
}

func (s *Store) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	var path string
	switch res {
	case storage.StorageResourcePackfile:
		path = s.realpath(fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac))
	case storage.StorageResourceState:
		path = s.realpath(fmt.Sprintf("states/%02x/%016x", mac[0], mac))
	case storage.StorageResourceLock:
		path = s.realpath(fmt.Sprintf("locks/%016x", mac))
	default:
		return nil, errors.ErrUnsupported
	}

	object, err := s.minioClient.GetObject(ctx, s.bucket, path, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s object: %w", res, err)
	}

	if rg != nil {
		return reading.NewSectionReadCloser(object, int64(rg.Offset), int64(rg.Length)), nil
	}

	return object, nil
}

func (s *Store) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	var path string
	switch res {
	case storage.StorageResourcePackfile:
		path = s.realpath(fmt.Sprintf("packfiles/%02x/%016x", mac[0], mac))
	case storage.StorageResourceState:
		path = s.realpath(fmt.Sprintf("states/%02x/%016x", mac[0], mac))
	case storage.StorageResourceLock:
		path = s.realpath(fmt.Sprintf("locks/%016x", mac))
	}

	err := s.minioClient.RemoveObject(ctx, s.bucket, path, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("remove %s object: %w", res, err)
	}
	return nil
}

func (s *Store) Close(ctx context.Context) error {
	return nil
}
