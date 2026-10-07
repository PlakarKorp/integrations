/*
 * Copyright (c) 2026 Stefan Sperling <stsp@stsp.name>
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

package main

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
)

const defaultMongoDBPort = 27017
const backupFilename = "mongodb-backup.bson"

type mongodbImporter struct {
	url             *url.URL
	port            string
	username        string
	password        string
	options         *connectors.Options
	use_tls         bool
	tls_ca_cert     string
	tls_client_cert string
	auth_mechanism  string
}

func init() {
	importer.Register("mongodb", 0, NewImporter)
}

func (i *mongodbImporter) Root() string {
	return "/"
}

func (i *mongodbImporter) Origin() string        { return i.url.Host }
func (i *mongodbImporter) Type() string          { return "mongodb" }
func (i *mongodbImporter) Flags() location.Flags { return location.FLAG_STREAM }

func NewImporter(ctx context.Context, opts *connectors.Options, proto string, params map[string]string) (importer.Importer, error) {
	location := params["location"]

	parsed, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("failed to parse location %s: %w", location, err)
	}

	use_tls, err := strconv.ParseBool(params["use_tls"])
	if err != nil {
		use_tls = true
	}

	port := params["port"]

	if len(port) == 0 {
		port = parsed.Port()
	}

	if len(port) == 0 {
		port = fmt.Sprintf("%d", defaultMongoDBPort)
	}

	i := &mongodbImporter{
		url:             parsed,
		port:            port,
		username:        params["username"],
		password:        params["password"],
		options:         opts,
		use_tls:         use_tls,
		tls_ca_cert:     params["tls_ca_cert"],
		tls_client_cert: params["tls_client_cert"],
		auth_mechanism:  params["auth_mechanism"],
	}

	return i, nil
}

func (i *mongodbImporter) Ping(ctx context.Context) error {
	var args []string

	if i.url.Scheme != "mongodb+srv" {
		args = append(args, "--port")
		args = append(args, i.port)
	}
	if i.use_tls {
		args = append(args, "--tls")
		if len(i.tls_ca_cert) > 0 {
			args = append(args, "--tlsCAFile")
			args = append(args, i.tls_ca_cert)
		}
		if len(i.tls_client_cert) > 0 {
			args = append(args, "--tlsCertificateKeyFile")
			args = append(args, i.tls_client_cert)
		}
	}
	if len(i.auth_mechanism) > 0 {
		args = append(args, "--authenticationMechanism")
		args = append(args, i.auth_mechanism)

	}
	args = append(args, "--eval")
	args = append(args, "db.runCommand({ hello: 1 })")

	args = append(args, fmt.Sprintf("%s://%s", i.url.Scheme, i.url.Hostname()))
	cmd := exec.Command("mongosh", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// reap process
	go func() { _ = cmd.Wait() }()

	buf, err := io.ReadAll(stdout)
	if err != nil {
		return err
	}

	if len(buf) > 0 {
		for line := range strings.Lines(string(buf)) {
			if strings.HasPrefix(line, "  ok: 1") {
				return nil
			}
		}
	} else {
		buf, err = io.ReadAll(stderr)
		if err != nil {
			return err
		}
	}

	return fmt.Errorf("Unexpected output from mongosh: '%s'", string(buf))
}

func cleanupTempFile(f *os.File) {
	if f != nil {
		os.Remove(f.Name())
		f.Close()
	}
}

type commandResult struct {
	stderr []byte
	err    error
	exit   bool
}

func (i *mongodbImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	var args []string
	var f *os.File
	var err error

	if i.url.Scheme != "mongodb+srv" {
		args = append(args, "--port")
		args = append(args, i.port)
	}
	if i.use_tls {
		args = append(args, "--ssl")
		if len(i.tls_ca_cert) > 0 {
			args = append(args, "--sslCAFile")
			args = append(args, i.tls_ca_cert)
		}
		if len(i.tls_client_cert) > 0 {
			args = append(args, "--sslPEMKeyFile")
			args = append(args, i.tls_client_cert)
		}
	}
	if len(i.auth_mechanism) > 0 {
		args = append(args, "--authenticationMechanism")
		args = append(args, i.auth_mechanism)
	}
	if len(i.username) > 0 {
		args = append(args, "--username")
		args = append(args, i.username)
	}
	if len(i.password) > 0 {
		f, err = os.CreateTemp("", "plakar-mongodb")
		if err != nil {
			return err
		}
		defer cleanupTempFile(f)

		escaped, err := json.Marshal(i.password)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(f, "password: %s\n", escaped); err != nil {
			return err
		}
		args = append(args, "--config")
		args = append(args, f.Name())
	}
	args = append(args, "--archive")

	args = append(args, fmt.Sprintf("%s://%s", i.url.Scheme, i.url.Hostname()))
	cmd := exec.Command("mongodump", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	read_stderr := func(c chan (commandResult)) {
		rd := bufio.NewReader(stderr)

		for {
			buf, err := rd.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					return
				}
				c <- commandResult{err: fmt.Errorf("%s", buf)}
				return
			}

			if len(buf) > 0 {
				c <- commandResult{stderr: buf}
			}
		}
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	c := make(chan commandResult, 1)

	// reap process
	go func() { err := cmd.Wait(); c <- commandResult{exit: true, err: err} }()

	fi := objects.FileInfo{
		Lname:      backupFilename,
		Lmode:      0644,
		Lsize:      -1,
		Ldev:       0,
		Lino:       0,
		Luid:       0,
		Lgid:       0,
		Lnlink:     0,
		LmodTime:   time.Now(),
		Lusername:  "",
		Lgroupname: "",
	}
	records <- connectors.NewRecord("/", "", fi, nil,
		func() (io.ReadCloser, error) { return io.NopCloser(stdout), nil })

	go func() {
		read_stderr(c)
	}()

	var res commandResult
	for err == nil && res.exit == false {
		select {
		case r := <-c:
			if len(r.stderr) > 0 {
				res.stderr = append(res.stderr, r.stderr...)
			}
			if res.exit == false {
				res.exit = r.exit
			}
			if r.err != nil {
				err = r.err
			}
		}
	}

	if err != nil && res.exit == true && len(res.stderr) > 0 {
		err = fmt.Errorf("%s: %s", err, res.stderr)
	}

	return err
}

func (i *mongodbImporter) Close(ctx context.Context) error {
	return nil
}

func main() {
	sdk.EntrypointImporter(os.Args, NewImporter)
}
