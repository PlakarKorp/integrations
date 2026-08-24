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
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"strconv"
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
	url     *url.URL
	params  map[string]string
	options *connectors.Options
	use_tls	bool
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

	i := &mongodbImporter{
		url:     parsed,
		params:  params,
		options: opts,
		use_tls: use_tls,
	}

	return i, nil
}

func (i *mongodbImporter) getPort() string {
	port := i.params["port"]

	if len(port) == 0 {
		port = i.url.Port()
	}

	if len(port) == 0 {
		port = fmt.Sprintf("%d", defaultMongoDBPort)
	}

	return port
}

func (i *mongodbImporter) Ping(ctx context.Context) error {
	var args []string

	args = append(args, "--host")
	args = append(args, i.url.Hostname())
	args = append(args, "--port")
	args = append(args, i.getPort())
	if i.use_tls {
		args = append(args, "--tls")
	}
	username := i.params["username"]
	if len(username) > 0 {
		args = append(args, "--username")
		args = append(args, username)
	}
	password := i.params["password"]
	if len(password) > 0 {
		args = append(args, "--password")
		args = append(args, password)
	}
	args = append(args, "--eval")
	args = append(args, "db.runCommand({ hello: 1 })")
	cmd := exec.Command("mongosh", args...)

	stdout, err := cmd.StdoutPipe()
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
	}

	return fmt.Errorf("Unexpected output from mongosh: '%s'", string(buf))
}

func (i *mongodbImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	var args []string

	args = append(args, "--host")
	args = append(args, i.url.Hostname())
	args = append(args, "--port")
	args = append(args, i.getPort())
	if i.use_tls {
		args = append(args, "--ssl")
	}
	username := i.params["username"]
	if len(username) > 0 {
		args = append(args, "--username")
		args = append(args, username)
	}
	password := i.params["password"]
	if len(password) > 0 {
		args = append(args, "--password")
		args = append(args, password)
	}
	args = append(args, "--archive")

	cmd := exec.Command("mongodump", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// reap process
	go func() { _ = cmd.Wait() }()

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

	return nil
}

func (i *mongodbImporter) Close(ctx context.Context) error {
	return nil
}

func main() {
	sdk.EntrypointImporter(os.Args, NewImporter)
}
