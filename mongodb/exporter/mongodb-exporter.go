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
	"io/ioutil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"strconv"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/location"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
)

const defaultMongoDBPort = 27017

type mongodbExporter struct {
	url     *url.URL
	params  map[string]string
	options *connectors.Options
	use_tls	bool
	stdin	io.WriteCloser
}

func init() {
	exporter.Register("mongodb", 0, NewExporter)
}

func (e *mongodbExporter) Root() string          { return "/" }
func (e *mongodbExporter) Origin() string        { return e.url.Host }
func (e *mongodbExporter) Type() string          { return "mongodb" }
func (e *mongodbExporter) Flags() location.Flags { return location.FLAG_STREAM }

func NewExporter(ctx context.Context, opts *connectors.Options, proto string, params map[string]string) (exporter.Exporter, error) {
	location := params["location"]

	parsed, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("failed to parse location %s: %w", location, err)
	}

	use_tls, err := strconv.ParseBool(params["use_tls"])
	if err != nil {
		use_tls = true
	}

	e := &mongodbExporter{
		url:     parsed,
		params:  params,
		options: opts,
		use_tls: use_tls,
	}

	return e, nil
}

func (e *mongodbExporter) getPort() string {
	port := e.params["port"]

	if len(port) == 0 {
		port = e.url.Port()
	}

	if len(port) == 0 {
		port = fmt.Sprintf("%d", defaultMongoDBPort)
	}

	return port
}

func (e *mongodbExporter) Ping(ctx context.Context) error {
	var args []string

	args = append(args, "--host")
	args = append(args, e.url.Hostname())
	args = append(args, "--port")
	args = append(args, e.getPort())
	if e.use_tls {
		args = append(args, "--tls")
	}
	username := e.params["username"]
	if len(username) > 0 {
		args = append(args, "--username")
		args = append(args, username)
	}
	password := e.params["password"]
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

	buf, err := ioutil.ReadAll(stdout)
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

func (e *mongodbExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {

	var args []string

	args = append(args, "--host")
	args = append(args, e.url.Hostname())
	args = append(args, "--port")
	args = append(args, e.getPort())
	if e.use_tls {
		args = append(args, "--ssl")
	}
	username := e.params["username"]
	if len(username) > 0 {
		args = append(args, "--username")
		args = append(args, username)
	}
	password := e.params["password"]
	if len(password) > 0 {
		args = append(args, "--password")
		args = append(args, password)
	}
	args = append(args, "--archive")

	cmd := exec.Command("mongorestore", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	e.stdin = stdin

	if err := cmd.Start(); err != nil {
		return err
	}

	// reap process
	go func() { _ = cmd.Wait() }()

	for record := range records {
		if record.Err != nil || !record.FileInfo.Mode().IsRegular() {
			results <- record.Ok()
			continue
		}

		if _, err := io.Copy(e.stdin, record.Reader); err != nil {
			results <- record.Error(err)
		} else {
			results <- record.Ok()
			break // There should only be one record
		}
	}

	return nil
}

func (e *mongodbExporter) Close(ctx context.Context) error {
	if e.stdin != nil {
		e.stdin.Close()
	}

	return nil
}

func main() {
	sdk.EntrypointExporter(os.Args, NewExporter)
}
