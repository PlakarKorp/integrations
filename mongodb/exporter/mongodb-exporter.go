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
	"fmt"
	"io"
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
const backupFilename = "mongodb-backup.bson"
const debug = false

type mongodbExporter struct {
	url     *url.URL
	port	string
	username string
	password string
	options *connectors.Options
	use_tls	bool
	stdin	io.WriteCloser
	stdout	io.ReadCloser
	stderr	io.ReadCloser
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

	port := params["port"]

	if len(port) == 0 {
		port = parsed.Port()
	}

	if len(port) == 0 {
		port = fmt.Sprintf("%d", defaultMongoDBPort)
	}

	e := &mongodbExporter{
		url:     parsed,
		port:    port,
		username: params["username"],
		password: params["password"],
		options: opts,
		use_tls: use_tls,
	}

	return e, nil
}

func (e *mongodbExporter) commonArgs() []string {
	var args []string

	args = append(args, "--host")
	args = append(args, e.url.Hostname())
	args = append(args, "--port")
	args = append(args, e.port)
	if e.use_tls {
		args = append(args, "--tls")
	}
	if len(e.username) > 0 {
		args = append(args, "--username")
		args = append(args, e.username)
	}
	if len(e.password) > 0 {
		args = append(args, "--password")
		args = append(args, e.password)
	}

	return args;
}

func (e *mongodbExporter) Ping(ctx context.Context) error {
	args := e.commonArgs()
	args = append(args, "--eval")
	args = append(args, "db.runCommand({ hello: 1 })")
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

type commandResult struct {
	stdout []byte
	stderr []byte
	err    error
	exit   bool
}

func (e *mongodbExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	args := e.commonArgs()
	args = append(args, "--drop")
	args = append(args, "--objcheck")
	args = append(args, "--archive")

	cmd := exec.Command("mongorestore", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	e.stdin = stdin

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	read_stdout := func(c chan (commandResult)) {
		rd := bufio.NewReader(stdout)

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
				c <- commandResult{stdout: buf}
			}
		}
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
	go func() { err := cmd.Wait(); c <- commandResult{exit: true, err : err} }()

	go func() {
		read_stdout(c)
	}()

	go func() {
		read_stderr(c)
	}()

	go func() {
		for record := range records {
			if record.Err != nil || !record.FileInfo.Mode().IsRegular() ||
			    strings.Compare(record.FileInfo.Name(), backupFilename) != 0 {
				results <- record.Ok()
				continue
			}

			if _, err := io.Copy(e.stdin, record.Reader); err != nil {
				results <- record.Error(err)
			} else {
				results <- record.Ok()
			}
		}

		e.stdin.Close()
		e.stdin = nil
	}()

	var res commandResult
	for err == nil && res.exit == false {
		select {
		case r := <-c:
			if len(r.stdout) > 0 {
				res.stdout = append(res.stdout, r.stdout...)
			}
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

	if debug && len(res.stdout) > 0 {
		fmt.Fprintf(os.Stderr, "%s", string(res.stdout))
	}
	if err != nil && res.exit == true && len(res.stderr) > 0 {
		if debug {
			fmt.Fprintf(os.Stderr, "%s", string(res.stderr))
		}
		err = fmt.Errorf("%s: %s", err, res.stderr)
	}

	return err
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
