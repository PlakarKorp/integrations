package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PlakarKorp/integration-sdk"
	"github.com/PlakarKorp/integration-sdk/location"
)

// maxConfigSize bounds the CONFIG read: it holds a small serialized
// repository configuration, never anywhere near this size.
const maxConfigSize = 2 << 10

// Every repository object is a tagged manifest with a single layer holding
// the object's bytes. The tag names the object: CONFIG, or the prefix of its
// resource followed by the hex MAC.
const (
	configTag         = "CONFIG"
	packfilePrefix    = "packfiles-"
	statePrefix       = "state-"
	lockPrefix        = "locks-"
	eccPackfilePrefix = "ecc-packfiles-"
	eccStatePrefix    = "ecc-state-"
)

// repositoryRe is the repository name grammar of the distribution spec.
var repositoryRe = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*$`)

func init() {
	_ = integration.RegisterStore("oci", 0, New)
}

type Store struct {
	reg  *registry
	repo string
}

func New(ctx context.Context, proto string, config map[string]string) (integration.Store, error) {
	insecure, err := boolOption(config, "insecure")
	if err != nil {
		return nil, err
	}
	noVerify, err := boolOption(config, "tls_insecure_no_verify")
	if err != nil {
		return nil, err
	}
	allowCrossHostUploads, err := boolOption(config, "allow_cross_host_uploads")
	if err != nil {
		return nil, err
	}
	if noVerify && insecure {
		return nil, errors.New("tls_insecure_no_verify is meaningless with insecure=true (no TLS to verify)")
	}

	username, password, token := config["username"], config["password"], config["token"]
	if token != "" && (username != "" || password != "") {
		return nil, errors.New("token and username/password are mutually exclusive")
	}
	if password != "" && username == "" {
		return nil, errors.New("password needs a username")
	}

	scheme := "https"
	if insecure {
		scheme = "http"
	}

	u, err := url.Parse(scheme + "://" + strings.TrimPrefix(config["location"], "oci://"))
	if err != nil {
		return nil, fmt.Errorf("parse location: %w", err)
	}
	if u.User != nil {
		return nil, errors.New("credentials go in username and password, not in the location")
	}
	if u.Host == "" {
		return nil, errors.New("location needs a registry host: oci://<host>[:<port>]/<repository>")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("location cannot carry a query or a fragment")
	}
	repo := strings.Trim(u.Path, "/")
	if repo == "" {
		return nil, errors.New("location needs a repository: oci://<host>[:<port>]/<repository>")
	}
	if !repositoryRe.MatchString(repo) {
		return nil, fmt.Errorf("invalid repository name %q", repo)
	}

	reg, err := newRegistry(&url.URL{Scheme: scheme, Host: strings.ToLower(u.Host)}, repo, registryOptions{
		insecure:              insecure,
		noVerify:              noVerify,
		allowCrossHostUploads: allowCrossHostUploads,
		username:              username,
		password:              password,
		token:                 token,
	})
	if err != nil {
		return nil, err
	}

	return &Store{reg: reg, repo: repo}, nil
}

func boolOption(config map[string]string, key string) (bool, error) {
	v := config[key]
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s value %q", key, v)
	}
	return b, nil
}

func (s *Store) Origin() string        { return s.reg.base.Host }
func (s *Store) Root() string          { return "/" + s.repo }
func (s *Store) Type() string          { return "oci" }
func (s *Store) Flags() location.Flags { return 0 }

func (s *Store) Mode(context.Context) (integration.Mode, error) {
	return integration.ModeRead | integration.ModeWrite, nil
}

func (s *Store) Size(context.Context) (int64, error) {
	return -1, nil
}

func (s *Store) Close(context.Context) error {
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.reg.ping(ctx)
}

func (s *Store) Create(ctx context.Context, config []byte) error {
	exists, err := s.reg.manifestExists(ctx, configTag)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("repository %s is already initialized: CONFIG exists", s.repo)
	}

	_, err = s.reg.putObject(ctx, configTag, bytes.NewReader(config))
	return err
}

func (s *Store) Open(ctx context.Context) ([]byte, error) {
	rc, err := s.reg.getObject(ctx, configTag, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(io.LimitReader(rc, maxConfigSize+1))
	if err != nil {
		return nil, fmt.Errorf("read CONFIG: %w", err)
	}
	if len(data) > maxConfigSize {
		return nil, fmt.Errorf("CONFIG exceeds %d bytes", maxConfigSize)
	}
	return data, nil
}

func resourcePrefix(res integration.StorageResource) (string, error) {
	switch res {
	case integration.StorageResourcePackfile:
		return packfilePrefix, nil
	case integration.StorageResourceState:
		return statePrefix, nil
	case integration.StorageResourceLock:
		return lockPrefix, nil
	case integration.StorageResourceECCPackfile:
		return eccPackfilePrefix, nil
	case integration.StorageResourceECCState:
		return eccStatePrefix, nil
	default:
		return "", errors.ErrUnsupported
	}
}

func tagOf(res integration.StorageResource, mac integration.MAC) (string, error) {
	prefix, err := resourcePrefix(res)
	if err != nil {
		return "", err
	}
	return prefix + mac.FormatHex(), nil
}

func (s *Store) List(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag) iter.Seq2[*integration.StorageResourceEntry, error] {
	return func(yield func(*integration.StorageResourceEntry, error) bool) {
		prefix, err := resourcePrefix(res)
		if err != nil {
			yield(nil, err)
			return
		}

		for tag, err := range s.reg.tags(ctx) {
			if err != nil {
				yield(nil, err)
				return
			}

			name, ok := strings.CutPrefix(tag, prefix)
			if !ok {
				continue
			}
			var mac integration.MAC
			if err := mac.ParseHex(name); err != nil {
				continue
			}
			if !yield(&integration.StorageResourceEntry{MAC: mac}, nil) {
				return
			}
		}
	}
}

func (s *Store) Put(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC, rd io.Reader) (int64, error) {
	tag, err := tagOf(res, mac)
	if err != nil {
		return -1, err
	}
	return s.reg.putObject(ctx, tag, rd)
}

func (s *Store) Get(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC, rg *integration.Range) (io.ReadCloser, error) {
	tag, err := tagOf(res, mac)
	if err != nil {
		return nil, err
	}
	if rg != nil && rg.Length == 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	return s.reg.getObject(ctx, tag, rg)
}

func (s *Store) Delete(ctx context.Context, res integration.StorageResource, _ integration.StorageFlag, mac integration.MAC) error {
	tag, err := tagOf(res, mac)
	if err != nil {
		return err
	}
	return s.reg.deleteObject(ctx, tag)
}
