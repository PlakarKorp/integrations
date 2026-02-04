package common

import (
	"crypto/tls"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const MB = 1048576

var ErrNotExist = fs.ErrNotExist

type ImapConnector struct {
	Address     string
	Username    string
	Password    string
	TlsMode     string
	TlsNoVerify bool
}

type ImapSession struct {
	Client         *imapclient.Client
	CurrentMailbox string
	buf            []byte
}

func (ic *ImapConnector) InitFromConfig(config map[string]string) error {
	location := config["location"]

	endpoint, err := url.Parse(location)
	if err != nil {
		return err
	}

	location = endpoint.Host
	ic.Address = location

	if endpoint.User != nil {
		if endpoint.User.Username() != "" {
			ic.Username = endpoint.User.Username()
		}
		if p, ok := endpoint.User.Password(); ok {
			ic.Password = p
		}
	} else {
		v, ok := config["username"]
		if !ok {
			return fmt.Errorf("Missing username")
		}
		ic.Username = v

		v, ok = config["password"]
		if !ok {
			return fmt.Errorf("Missing password")
		}
		ic.Password = v
	}

	v, ok := config["tls"]
	if !ok {
		v = "starttls"
	}
	ic.TlsMode = v

	v, ok = config["tls_no_verify"]
	if v == "true" {
		ic.TlsNoVerify = true
	}

	return nil
}

func (imp *ImapConnector) Connect() (*ImapSession, error) {
	dialer := imapclient.DialTLS
	switch imp.TlsMode {
	case "no-tls":
		dialer = imapclient.DialInsecure
	case "starttls":
		dialer = imapclient.DialStartTLS
	case "tls":
		dialer = imapclient.DialTLS
	default:
		return nil, fmt.Errorf("Invalid tls mode %q", imp.TlsMode)
	}

	tlsCfg := &tls.Config{
		InsecureSkipVerify: imp.TlsNoVerify,
	}

	opts := &imapclient.Options{
		TLSConfig: tlsCfg,
	}

	client, err := dialer(imp.Address, opts)
	if err != nil {
		return nil, fmt.Errorf("Failed to dial IMAP server: %w", err)
	}

	err = client.Login(imp.Username, imp.Password).Wait()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("Failed to login %w", err)
	}

	return &ImapSession{
		Client: client,
		buf:    make([]byte, 2*MB),
	}, nil
}

func (session *ImapSession) Select(mailbox string, readOnly bool) error {
	_, err := session.Client.Select(mailbox, &imap.SelectOptions{
		ReadOnly: readOnly,
	}).Wait()
	if err != nil {
		return fmt.Errorf("SELECT command failed: %w", err)
	}
	return nil
}

func (session *ImapSession) Create(mailbox string, existOk bool) error {
	opts := imap.CreateOptions{}
	err := session.Client.Create(mailbox, &opts).Wait()
	if err != nil {
		e, ok := err.(*imap.Error)
		if ok && e.Code == imap.ResponseCodeAlreadyExists && existOk {
			return nil
		}
		return err
	}
	return nil
}

func (session *ImapSession) List() ([]*imap.ListData, error) {
	// Basic RFC3501 LIST, compatible with most servers
	res, err := session.Client.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("LIST command failed: %w", err)
	}
	return res, nil
}

func (session *ImapSession) Append(mailbox string, fp io.Reader, size int64) error {
	opts := imap.AppendOptions{}
	appendCmd := session.Client.Append(mailbox, size, &opts)
	w, err := io.CopyBuffer(appendCmd, fp, session.buf)
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}
	if w != size {
		return fmt.Errorf("inconsistent number of bytes written")
	}
	err = appendCmd.Close()
	if err != nil {
		return fmt.Errorf("failed to close message: %w", err)
	}
	_, err = appendCmd.Wait()
	if err != nil {
		return fmt.Errorf("APPEND command failed: %w", err)
	}

	return nil
}

func (session *ImapSession) Logout() error {
	return session.Client.Logout().Wait()
}

func SafeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "message"
	}

	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_' || r == '.':
			return r
		default:
			return '_'
		}
	}, s)
	s = strings.Join(strings.Fields(s), " ") // collapse spaces
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
