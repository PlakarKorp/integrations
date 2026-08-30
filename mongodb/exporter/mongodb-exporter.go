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
	"strconv"
	"strings"

	"github.com/PlakarKorp/integrations-private/mongodb/common"
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
	creds   common.Credentials
	options *connectors.Options
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	stderr  io.ReadCloser

	// cleanup removes the credentials file once mongorestore has finished
	// with it.
	cleanup func()
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
		url: parsed,
		creds: common.Credentials{
			Host:     parsed.Hostname(),
			Port:     port,
			Username: params["username"],
			Password: params["password"],
			TLS:      use_tls,
		},
		options: opts,
		cleanup: func() {},
	}

	return e, nil
}

func (e *mongodbExporter) Ping(ctx context.Context) error {
	// Fed over stdin rather than --eval so the credentials in the connection
	// URI never appear in argv.
	cmd := exec.CommandContext(ctx, "mongosh", "--quiet", "--nodb")
	cmd.Stdin = strings.NewReader(e.creds.PingScript())

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mongosh: %w: %s", err, strings.TrimSpace(string(out)))
	}

	if strings.Contains(string(out), common.PingOK) {
		return nil
	}

	return fmt.Errorf("unexpected output from mongosh: %q", string(out))
}

type commandResult struct {
	stdout []byte
	stderr []byte
	err    error
	exit   bool
}

func (e *mongodbExporter) Export(ctx context.Context, records <-chan *connectors.Record, results chan<- *connectors.Result) error {
	defer close(results)

	// mongorestore reads "password:" from the file named by --config, so the
	// password stays out of argv.
	cfgArg, cleanup, err := e.creds.ConfigFile()
	if err != nil {
		return err
	}
	e.cleanup = cleanup

	args := e.creds.Args()
	if cfgArg != "" {
		args = append(args, cfgArg)
	}
	args = append(args, "--drop")
	args = append(args, "--objcheck")
	args = append(args, "--archive")

	cmd := exec.CommandContext(ctx, "mongorestore", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
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
	go func() { _ = cmd.Wait(); c <- commandResult{exit: true} }()

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
	if err == nil && len(res.stderr) > 0 {
		if debug {
			fmt.Fprintf(os.Stderr, "%s", string(res.stderr))
		}
		err = fmt.Errorf("%s", res.stderr)
	}

	return err
}

func (e *mongodbExporter) Close(ctx context.Context) error {
	if e.stdin != nil {
		e.stdin.Close()
	}

	if e.cleanup != nil {
		e.cleanup()
	}

	return nil
}

func main() {
	sdk.EntrypointExporter(os.Args, NewExporter)
}
