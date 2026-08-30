package routeros

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Mode string

const (
	ModeExport Mode = "export"
	ModeBackup Mode = "backup"
)

var ErrAlreadyDone = errors.New("restore already done")

type Routeros struct {
	addr        string
	user        string
	authMethod  ssh.AuthMethod
	hostKeyCall ssh.HostKeyCallback

	mode Mode // for backup

	dryRun bool // for export

	client *ssh.Client
	mtx    sync.Mutex
}

func init() {
	importer.Register("routeros+export", 0, NewImporter)
	importer.Register("routeros+backup", 0, NewImporter)
	exporter.Register("routeros", 0, NewExporter)
}

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (importer.Importer, error) {
	return New(ctx, opts, proto, config, true)
}

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, config map[string]string) (exporter.Exporter, error) {
	return New(ctx, opts, proto, config, false)
}

func New(ctx context.Context, opts *connectors.Options, proto string, config map[string]string, importerp bool) (*Routeros, error) {
	loc, err := url.Parse(config["location"])
	if err != nil {
		return nil, fmt.Errorf("bad location %q: %w",
			config["location"], err)
	}

	user := loc.User.Username()
	if user == "" {
		if u, ok := config["user"]; ok {
			user = u
		}
	}
	if user == "" {
		return nil, fmt.Errorf("user not specified")
	}

	var authm ssh.AuthMethod

	if p, ok := config["password"]; ok {
		authm = ssh.Password(p)
	} else if p, ok := loc.User.Password(); ok {
		authm = ssh.Password(p)
	} else if k, ok := config["private_key"]; ok {
		pass := config["private_key_passphrase"]
		c, err := os.ReadFile(k)
		if err != nil {
			return nil, fmt.Errorf("failed to open private key %s: %w",
				k, err)
		}

		var signer ssh.Signer
		if pass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(c, []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey(c)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key %s: %w",
				k, err)
		}

		authm = ssh.PublicKeys(signer)
	}

	var mode Mode
	var dryrun bool

	if importerp {
		switch proto {
		case "routeros+export":
			mode = ModeExport
		case "routeros+backup":
			mode = ModeBackup
		default:
			return nil, fmt.Errorf("invalid protocol %q for importer", proto)
		}
	} else {
		if proto != "routeros" {
			return nil, fmt.Errorf("invalid protocol %q for exporter", proto)
		}

		if p, ok := config["dry_run"]; ok {
			dryrun, err = strconv.ParseBool(p)
			if err != nil {
				return nil, fmt.Errorf("invalid dry_run option: %w", err)
			}
		}
	}

	hostKeyCall, err := hostKeyCallback(config)
	if err != nil {
		return nil, err
	}

	host := loc.Host
	if loc.Port() == "" {
		host += ":22"
	}

	return &Routeros{
		addr:        host,
		user:        user,
		authMethod:  authm,
		hostKeyCall: hostKeyCall,
		mode:        mode,
		dryRun:      dryrun,
	}, nil
}

func (m *Routeros) Root() string          { return "/" }
func (m *Routeros) Origin() string        { return m.addr }
func (m *Routeros) Type() string          { return "routeros" }
func (m *Routeros) Flags() location.Flags { return location.FLAG_STREAM | location.FLAG_NEEDACK }

func (m *Routeros) connect() error {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if m.client != nil {
		return nil
	}

	client, err := ssh.Dial("tcp", m.addr, &ssh.ClientConfig{
		User:            m.user,
		Auth:            []ssh.AuthMethod{m.authMethod},
		HostKeyCallback: m.hostKeyCall,
		Timeout:         10 * time.Second,
		AuthCallback:    nil,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", m.addr, err)
	}

	m.client = client
	return nil
}

func (m *Routeros) exec(cmd string) error {
	if err := m.connect(); err != nil {
		return err
	}

	session, err := m.client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to open session: %w", err)
	}

	defer session.Close()

	return session.Run(cmd)
}

func (m *Routeros) Ping(ctx context.Context) error {
	return m.exec(":put ok")
}

// waitStable polls filename's size until it stops changing across two
// consecutive checks, or times out.
func (m *Routeros) waitStable(sftpClient *sftp.Client, filename string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastSize int64 = -1

	for time.Now().Before(deadline) {
		info, err := sftpClient.Stat(filename)
		if err != nil {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if info.Size() == lastSize && lastSize != 0 {
			return nil
		}
		lastSize = info.Size()
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %q to stabilize", filename)
}

func (m *Routeros) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	now := time.Now()

	filename := fmt.Sprintf("cfgdump-%d", now.Unix())
	var cmd string
	switch m.mode {
	case ModeExport:
		filename += ".rsc"
		cmd = fmt.Sprintf("/export file=%s", filename)
	case ModeBackup:
		filename += ".backup"
		cmd = fmt.Sprintf("/system backup save name=%s", filename)
	default:
		return fmt.Errorf("unknown mode %q", m.mode)
	}

	if err := m.exec(cmd); err != nil {
		return fmt.Errorf("exec %q: %w", cmd, err)
	}

	sftpClient, err := sftp.NewClient(m.client)
	if err != nil {
		return fmt.Errorf("sftp client: %w", err)
	}
	defer sftpClient.Close()

	defer sftpClient.Remove(filename) // best effort

	if m.mode == ModeBackup {
		if err := m.waitStable(sftpClient, filename, 10*time.Second); err != nil {
			return fmt.Errorf("wait for backup file: %w", err)
		}
	}

	f, err := sftpClient.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open %s file %s: %w", m.mode, filename, err)
	}
	defer f.Close() // safe to double-close

	records <- connectors.NewRecord("/"+filename, "", objects.FileInfo{
		Lname:    filename,
		Lsize:    -1,
		Lmode:    0644,
		LmodTime: now,
	}, nil, func() (io.ReadCloser, error) { return f, nil })

	return (<-results).Err
}

func (m *Routeros) restoreRsc(src io.Reader) error {
	remoteName := "plakar-restore.rsc"

	sftpClient, err := sftp.NewClient(m.client)
	if err != nil {
		return fmt.Errorf("sftp client: %w", err)
	}
	defer sftpClient.Close()

	dst, err := sftpClient.Create(remoteName)
	if err != nil {
		return fmt.Errorf("create remote file: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("upload: %w", err)
	}

	if err := dst.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	cmd := "import" // add verbose=yes?
	if m.dryRun {
		cmd += " dry-run=yes"
	}
	cmd += fmt.Sprintf(" file=%s", remoteName)

	if err := m.exec(cmd); err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	return nil
}

func (m *Routeros) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	if err := m.connect(); err != nil {
		return err
	}

	/* we expect just one .rsc or .backup file */

	done := false
	for record := range records {
		if record.Err != nil || !record.FileInfo.Lmode.IsRegular() {
			results <- record.Ok()
			continue
		}

		switch path.Ext(record.FileInfo.Lname) {
		case ".rsc":
			if done {
				results <- record.Error(ErrAlreadyDone)
				continue
			}
			done = true
			results <- record.Error(m.restoreRsc(record.Reader))
		case ".backup":
			if done {
				results <- record.Error(ErrAlreadyDone)
				continue
			}
			done = true
			results <- record.Error(errors.ErrUnsupported)
		default:
			results <- record.Ok()
		}
	}

	return nil
}

func (m *Routeros) Close(ctx context.Context) error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}
