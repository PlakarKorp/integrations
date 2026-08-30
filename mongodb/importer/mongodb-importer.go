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
	"strconv"
	"strings"
	"time"

	"github.com/PlakarKorp/integrations-private/mongodb/common"
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
	creds   common.Credentials
	options *connectors.Options
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
		url: parsed,
		creds: common.Credentials{
			Host:     parsed.Hostname(),
			Port:     port,
			Username: params["username"],
			Password: params["password"],
			TLS:      use_tls,
		},
		options: opts,
	}

	return i, nil
}

func (i *mongodbImporter) Ping(ctx context.Context) error {
	// The script is fed over stdin rather than --eval so the credentials in
	// the connection URI never appear in argv, where any local user could
	// read them out of ps.
	cmd := exec.CommandContext(ctx, "mongosh", "--quiet", "--nodb")
	cmd.Stdin = strings.NewReader(i.creds.PingScript())

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mongosh: %w: %s", err, strings.TrimSpace(string(out)))
	}

	if strings.Contains(string(out), common.PingOK) {
		return nil
	}

	return fmt.Errorf("unexpected output from mongosh: %q", string(out))
}

func (i *mongodbImporter) Import(ctx context.Context, records chan<- *connectors.Record, results <-chan *connectors.Result) error {
	defer close(records)

	// mongodump reads "password:" from the file named by --config, so the
	// password stays out of argv.
	cfgArg, cleanup, err := i.creds.ConfigFile()
	if err != nil {
		return err
	}

	args := i.creds.Args()
	if cfgArg != "" {
		args = append(args, cfgArg)
	}
	args = append(args, "--archive")

	cmd := exec.CommandContext(ctx, "mongodump", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return err
	}

	if err := cmd.Start(); err != nil {
		cleanup()
		return err
	}

	// reap process, and only drop the credentials file once mongodump has
	// finished reading it
	go func() {
		_ = cmd.Wait()
		cleanup()
	}()

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
