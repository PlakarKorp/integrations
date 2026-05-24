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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

type Store struct {
	config     storage.Configuration
	Repository string
	location   *url.URL
	authToken  string
}

func init() {
	storage.Register("http", 0, NewStore)
	storage.Register("https", 0, NewStore)
}

func NewStore(ctx context.Context, proto string, storeConfig map[string]string) (storage.Store, error) {
	location, err := url.Parse(storeConfig["location"])
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", storeConfig["location"], err)
	}

	return &Store{
		location:  location,
		authToken: storeConfig["auth_token"],
	}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return nil
}

func (s *Store) Origin() string        { return s.location.Host }
func (s *Store) Root() string          { return s.location.Path }
func (s *Store) Type() string          { return "http" }
func (s *Store) Flags() location.Flags { return 0 }

func (s *Store) sendRequest(method string, requestType string, payload io.Reader, rg *storage.Range) (*http.Response, error) {
	u := *s.location
	u.Path = path.Join(u.Path, requestType)
	req, err := http.NewRequest(method, u.String(), payload)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if s.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.authToken)
	}
	if rg != nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rg.Offset, rg.Offset+uint64(rg.Length)))
	}

	return http.DefaultClient.Do(req)
}

func (s *Store) Create(ctx context.Context, config []byte) error {
	return nil
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	r, err := s.sendRequest("GET", "/", nil, nil)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()

	if r.StatusCode != 200 {
		errmsg, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s", errmsg)
	}

	return io.ReadAll(r.Body)
}

func (s *Store) Close(ctx context.Context) error {
	return nil
}

func (s *Store) Mode(ctx context.Context) (storage.Mode, error) {
	return storage.ModeRead | storage.ModeWrite, nil
}

func (s *Store) Size(ctx context.Context) (int64, error) {
	return -1, nil
}

func (s *Store) List(ctx context.Context, res storage.StorageResource) ([]objects.MAC, error) {
	uri := "/resources/" + strres(res)
	r, err := s.sendRequest("GET", uri, nil, nil)
	if err != nil {
		return nil, err
	}

	if r.StatusCode != 200 {
		errmsg, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s", errmsg)
	}

	var ret []objects.MAC
	if err := json.NewDecoder(r.Body).Decode(&ret); err != nil {
		return nil, err
	} else {
		return ret, nil
	}
}

func (s *Store) Put(ctx context.Context, res storage.StorageResource, mac objects.MAC, rd io.Reader) (int64, error) {
	uri := fmt.Sprintf("/resources/%s/%016x", strres(res), mac)
	cr := &countingReader{rc: rd}
	r, err := s.sendRequest("PUT", uri, cr, nil)
	if err != nil {
		return -1, err
	}
	defer r.Body.Close()

	if r.StatusCode != 200 {
		errmsg, err := io.ReadAll(r.Body)
		if err != nil {
			return -1, err
		}
		return -1, fmt.Errorf("%s", errmsg)
	}

	return cr.n, nil
}

func (s *Store) Get(ctx context.Context, res storage.StorageResource, mac objects.MAC, rg *storage.Range) (io.ReadCloser, error) {
	uri := fmt.Sprintf("/resources/%s/%016x", strres(res), mac)
	r, err := s.sendRequest("GET", uri, nil, rg)
	if err != nil {
		return nil, err
	}

	if r.StatusCode != 200 {
		errmsg, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s", errmsg)
	}
	return r.Body, nil
}

func (s *Store) Delete(ctx context.Context, res storage.StorageResource, mac objects.MAC) error {
	uri := fmt.Sprintf("/resources/%s/%016x", strres(res), mac)
	r, err := s.sendRequest("DELETE", uri, nil, nil)
	if err != nil {
		return err
	}
	defer r.Body.Close()

	if r.StatusCode != 200 {
		errmsg, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s", errmsg)
	}
	return nil
}

// we need a stringer on that enum
func strres(s storage.StorageResource) string {
	switch s {
	case storage.StorageResourceUndefined:
		return "undefined"
	case storage.StorageResourcePackfile:
		return "packfiles"
	case storage.StorageResourceState:
		return "states"
	case storage.StorageResourceLock:
		return "locks"
	case storage.StorageResourceECCPackfile:
		return "eccpackfiles"
	case storage.StorageResourceECCState:
		return "eccstates"
	default:
		return ""
	}
}

type countingReader struct {
	rc io.Reader
	n  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	k, err := c.rc.Read(p)
	c.n += int64(k)
	return k, err
}
