package common

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"
)

type ImapConnector struct {
	Address     string
	Username    string
	Password    string
	TlsMode     string
	TlsNoVerify bool
}

func (ic *ImapConnector) InitFromConfig(config map[string]string) error {
	location := config["location"]
	location, _ = strings.CutPrefix(location, "imap://")
	ic.Address = location

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

	v, ok = config["tls"]
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

func (imp *ImapConnector) Connect() (*imapclient.Client, error) {
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

	return client, nil
}
