package etcd

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
)

// etcdTLS builds the client TLS configuration.  Returning nil leaves the
// clientv3 default, which uses the system roots for an https endpoint.
func etcdTLS(config map[string]string) (*tls.Config, error) {
	var (
		caFile   = config["ca_file"]
		certFile = config["cert_file"]
		keyFile  = config["key_file"]

		noVerify bool
	)

	if v := config["tls_insecure_no_verify"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid tls_insecure_no_verify value %q", v)
		}
		noVerify = b
	}

	if caFile == "" && certFile == "" && keyFile == "" && !noVerify {
		return nil, nil
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: noVerify,
	}

	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %s contains no certificate", caFile)
		}
		cfg.RootCAs = pool
	}

	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("cert_file and key_file must be given together")
	}
	if certFile != "" {
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("loading client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}

	return cfg, nil
}
