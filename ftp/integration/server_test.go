// Package integration runs the importer and exporter against a real FTP
// server in a container.
package integration

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	image    = "stilliard/pure-ftpd@sha256:6275c1cf4e30c305ae2aa5077ec825ee8c2b2eb936e1634487213ee6ce4073ed"
	username = "plakar"
	password = "plakar"

	// pure-pw hashes with scrypt at sensitive limits, which costs about 3s per
	// login.  A sha512-crypt entry of password keeps the tests quick.
	passwd = username + ":$6$plakartestsalt$Zv0pZvXXzrQcKUzvM7Fy5FDkc57LcLk0AZTU7eUJ5cfC57IvNJs/CLG6hEQNHt6Du8/tIBUsM6RqM7qceO587/" +
		":1000:1000::/home/ftpusers/" + username + "/./::::::::::::\n"

	// The server announces passive ports in its PASV replies, so they are
	// bound on the host under the same numbers.  Parallel tests each hold up
	// to a pool of goftp connections, hence the room here and in the client
	// limits below.
	passiveCount = 50
)

var (
	serverOnce sync.Once
	serverCtr  testcontainers.Container
	serverAddr string
	serverErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if serverCtr != nil {
		_ = serverCtr.Terminate(context.Background(), testcontainers.StopTimeout(0))
	}
	os.Exit(code)
}

// server returns the host:port of the shared FTP server, starting it on first
// use.  It serves both plain FTP and explicit AUTH TLS with a self-signed
// certificate.
func server(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("needs docker")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	serverOnce.Do(func() {
		serverAddr, serverErr = startServer(context.Background())
	})
	require.NoError(t, serverErr)
	return serverAddr
}

// freePortRange finds n consecutive ports free on 127.0.0.1.  Another process
// can still take one before the container binds it; that fails loudly.
func freePortRange(n int) (int, error) {
	for range 100 {
		first := 40000 + rand.IntN(20000)
		var ls []net.Listener
		for p := first; p < first+n; p++ {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
			if err != nil {
				break
			}
			ls = append(ls, l)
		}
		for _, l := range ls {
			_ = l.Close()
		}
		if len(ls) == n {
			return first, nil
		}
	}
	return 0, errors.New("no free passive port range")
}

func startServer(ctx context.Context) (string, error) {
	first, err := freePortRange(passiveCount)
	if err != nil {
		return "", err
	}
	last := first + passiveCount - 1

	exposed := []string{"21/tcp"}
	for p := first; p <= last; p++ {
		exposed = append(exposed, fmt.Sprintf("%d/tcp", p))
	}

	req := testcontainers.ContainerRequest{
		Image:        image,
		ExposedPorts: exposed,
		// The image default, without -R: it refuses SITE CHMOD.
		Cmd: []string{"/bin/sh", "-c", "/run.sh -l puredb:/etc/pure-ftpd/pureftpd.pdb -E -j -P $PUBLICHOST"},
		HostConfigModifier: func(hc *container.HostConfig) {
			if hc.PortBindings == nil {
				hc.PortBindings = network.PortMap{}
			}
			for p := first; p <= last; p++ {
				hc.PortBindings[network.MustParsePort(fmt.Sprintf("%d/tcp", p))] = []network.PortBinding{{
					HostIP:   netip.MustParseAddr("127.0.0.1"),
					HostPort: strconv.Itoa(p),
				}}
			}
		},
		Env: map[string]string{
			"PUBLICHOST":          "127.0.0.1",
			"FTP_PASSIVE_PORTS":   fmt.Sprintf("%d:%d", first, last),
			"FTP_MAX_CLIENTS":     "200",
			"FTP_MAX_CONNECTIONS": "200",
			"ADDED_FLAGS":         "--tls=1",
			"TLS_CN":              "localhost",
			"TLS_ORG":             "plakar",
			"TLS_C":               "FR",
			"TLS_USE_DSAPRAM":     "true",
		},
		Files: []testcontainers.ContainerFile{{
			Reader:            strings.NewReader(passwd),
			ContainerFilePath: "/etc/pure-ftpd/passwd/pureftpd.passwd",
			FileMode:          0o600,
		}},
		WaitingFor: wait.ForLog("Starting Pure-FTPd"),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	serverCtr = ctr
	if err != nil {
		return "", err
	}

	port, err := ctr.MappedPort(ctx, "21/tcp")
	if err != nil {
		return "", err
	}
	return "127.0.0.1:" + port.Port(), nil
}
