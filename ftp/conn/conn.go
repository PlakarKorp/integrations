package conn

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/secsy/goftp"
)

// TLS selects the transport: TLSExplicit upgrades the control connection
// with AUTH TLS, TLSImplicit wraps it from the start, TLSNone is plain FTP.
type TLS string

const (
	TLSExplicit TLS = "explicit"
	TLSImplicit TLS = "implicit"
	TLSNone     TLS = "none"
)

// Options carries the connection settings shared by the importer and the
// exporter.
type Options struct {
	Username string
	Password string

	TLS TLS

	// InsecureSkipVerify disables certificate verification.  Only honoured
	// when TLS is in use.
	InsecureSkipVerify bool
}

// ParseOptions reads the connection settings out of a connector config map.
//
// FTP has no transport security of its own: credentials and backup data both
// travel in the clear.  The default is therefore "explicit" (AUTH TLS), and
// falling back to plain FTP has to be asked for, matching how the webdav
// connector gates dav:// behind insecure=true.
func ParseOptions(config map[string]string) (Options, error) {
	opts := Options{
		Username: config["username"],
		Password: config["password"],
		TLS:      TLSExplicit,
	}

	if v, ok := config["tls"]; ok && v != "" {
		switch t := TLS(v); t {
		case TLSExplicit, TLSImplicit, TLSNone:
			opts.TLS = t
		default:
			return Options{}, fmt.Errorf("invalid tls value %q (accepted: explicit, implicit, none)", v)
		}
	}

	if v, ok := config["tls_insecure_no_verify"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Options{}, fmt.Errorf("invalid tls_insecure_no_verify value %q", v)
		}
		opts.InsecureSkipVerify = b
	}

	if opts.TLS == TLSNone && !opts.InsecureSkipVerify {
		return Options{}, fmt.Errorf("tls=none sends credentials and data in cleartext; " +
			"set tls_insecure_no_verify=true as well to acknowledge it, or use tls=explicit")
	}

	return opts, nil
}

func ConnectToFTP(host string, opts Options) (*goftp.Client, error) {
	config := goftp.Config{
		User:     opts.Username,
		Password: opts.Password,
		Timeout:  10 * time.Second,
	}

	if opts.TLS != TLSNone {
		hostname := host
		if h, _, err := net.SplitHostPort(host); err == nil {
			hostname = h
		}

		config.TLSConfig = &tls.Config{
			ServerName:         hostname,
			InsecureSkipVerify: opts.InsecureSkipVerify, //nolint:gosec // opt-in, see ParseOptions
			MinVersion:         tls.VersionTLS12,
		}
		if opts.TLS == TLSImplicit {
			config.TLSMode = goftp.TLSImplicit
		} else {
			config.TLSMode = goftp.TLSExplicit
		}
	}

	return goftp.DialConfig(config, host)
}
