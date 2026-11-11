/*
 * Copyright (c) 2026 Gilles Chehade <gilles@plakar.io>
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

package docker

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"path"
	"runtime"
	"strings"
	"time"

	gexporter "github.com/PlakarKorp/integration-grpc/exporter"
	gimporter "github.com/PlakarKorp/integration-grpc/importer"
	"github.com/PlakarKorp/integrations/docker/mtls"
	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/connectors/exporter"
	"github.com/PlakarKorp/kloset/connectors/importer"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// path at which the volume is mounted inside the helper.
const dataPath = "/data"

// helper is a running kubelet container with one volume mounted at /data,
// serving plakar's fs importer (backup) or exporter (restore) on a port
// published on the daemon host's loopback.
//
// TODO: publishing a port only works when the daemon runs on this machine
// (Docker Desktop included).  Over DOCKER_HOST=ssh:// the port is on the
// remote host; the SDK's StdioConn over the attach stream would do away
// with the port and the mTLS altogether.
type helper struct {
	cli  *client.Client
	id   string
	addr string
	cert tls.Certificate
	peer [32]byte

	// the backup side keeps the importer and its connection open until
	// the helper is stopped: kloset reads the content through them
	// after the records went by.
	conn *grpc.ClientConn
	imp  importer.Importer
}

func (d *Docker) ensureImage(ctx context.Context) error {
	if _, err := d.cli.ImageInspect(ctx, d.kubeletImage); err == nil {
		return nil
	}
	rc, err := d.cli.ImagePull(ctx, d.kubeletImage, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", d.kubeletImage, err)
	}
	defer rc.Close()
	if err := rc.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", d.kubeletImage, err)
	}
	return nil
}

func (d *Docker) startHelper(ctx context.Context, vol string, readOnly bool) (*helper, error) {
	if err := d.ensureImage(ctx); err != nil {
		return nil, err
	}

	cert, fp, err := mtls.Gencert()
	if err != nil {
		return nil, fmt.Errorf("generate certificate: %w", err)
	}

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}

	port := network.MustParsePort("8080/tcp")
	cmd := []string{"-p", "8080", "-peer", mtls.Fingerprint(fp)}
	if !readOnly {
		cmd = append([]string{"-export"}, cmd...)
	}
	mount := vol + ":" + dataPath
	if readOnly {
		mount += ":ro"
	}

	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: "plakar-docker-" + hex.EncodeToString(suffix[:]),
		Config: &container.Config{
			Image:        d.kubeletImage,
			Cmd:          cmd,
			ExposedPorts: network.PortSet{port: struct{}{}},
			Labels:       map[string]string{helperLabel: vol},
		},
		HostConfig: &container.HostConfig{
			Binds: []string{mount},
			PortBindings: network.PortMap{
				port: []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create helper for %s: %w", vol, err)
	}

	h := &helper{cli: d.cli, id: created.ID, cert: cert}

	if _, err := d.cli.ContainerStart(ctx, h.id, client.ContainerStartOptions{}); err != nil {
		h.stop(ctx)
		return nil, fmt.Errorf("start helper for %s: %w", vol, err)
	}

	// the image prints its key before it listens: wait for both.
	deadline := time.Now().Add(30 * time.Second)
	for {
		logs, err := d.helperLogs(ctx, h.id)
		if err != nil {
			h.stop(ctx)
			return nil, err
		}
		if strings.Contains(logs, "listening on") {
			for _, line := range strings.Split(logs, "\n") {
				// the frame header precedes the text on each line
				if i := strings.Index(line, "plakar-pubkey: "); i >= 0 {
					rest := line[i+len("plakar-pubkey: "):]
					h.peer, err = mtls.ParseFingerprint(strings.TrimSpace(rest))
					if err != nil {
						h.stop(ctx)
						return nil, fmt.Errorf("helper for %s: %w", vol, err)
					}
				}
			}
			break
		}
		if time.Now().After(deadline) {
			h.stop(ctx)
			return nil, fmt.Errorf("helper for %s did not come up: %s", vol, strings.TrimSpace(logs))
		}
		select {
		case <-ctx.Done():
			h.stop(ctx)
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if h.peer == [32]byte{} {
		h.stop(ctx)
		return nil, fmt.Errorf("helper for %s never announced its key", vol)
	}

	ins, err := d.cli.ContainerInspect(ctx, h.id, client.ContainerInspectOptions{})
	if err != nil {
		h.stop(ctx)
		return nil, err
	}
	bindings := ins.Container.NetworkSettings.Ports[port]
	if len(bindings) == 0 {
		h.stop(ctx)
		return nil, fmt.Errorf("helper for %s: no port published", vol)
	}
	h.addr = "127.0.0.1:" + bindings[0].HostPort
	return h, nil
}

// helperLogs returns what the helper wrote so far.  Without a TTY the
// stream carries 8-byte frame headers, which do not get in the way of the
// substring searches made on it.
func (d *Docker) helperLogs(ctx context.Context, id string) (string, error) {
	rc, err := d.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 64<<10))
	return string(b), err
}

func (h *helper) dial() (*grpc.ClientConn, error) {
	cred := credentials.NewTLS(mtls.ClientTlsConfig(&h.cert, h.peer))
	return grpc.NewClient(h.addr, grpc.WithTransportCredentials(cred))
}

func (h *helper) stop(ctx context.Context) {
	if h.imp != nil {
		_ = h.imp.Close(ctx)
		h.imp = nil
	}
	if h.conn != nil {
		_ = h.conn.Close()
		h.conn = nil
	}
	if h.cli == nil || h.id == "" {
		return
	}
	_, _ = h.cli.ContainerRemove(context.WithoutCancel(ctx), h.id, client.ContainerRemoveOptions{Force: true})
	h.id = ""
}

func (d *Docker) remoteOptions() *connectors.Options {
	return &connectors.Options{
		Hostname:        "plakar-helper",
		OperatingSystem: "linux",
		Architecture:    runtime.GOARCH,
		CWD:             dataPath,
		MaxConcurrency:  d.opts.MaxConcurrency,
	}
}

// consume runs the fs importer inside h and republishes its records under
// prefix.  It returns once the helper sent its last record; the helper
// itself stays up, kloset reads the content afterwards.
func (d *Docker) consume(ctx context.Context, r *relay, h *helper, prefix string) error {
	conn, err := h.dial()
	if err != nil {
		return fmt.Errorf("dial helper: %w", err)
	}
	h.conn = conn

	imp, err := gimporter.NewImporter(ctx, conn, d.remoteOptions(), "fs", map[string]string{
		"location":         "fs://" + dataPath,
		"dont_traverse_fs": "true",
	})
	if err != nil {
		return fmt.Errorf("instantiate the helper importer: %w", err)
	}
	h.imp = imp

	var (
		records = make(chan *connectors.Record, 64)
		results = make(chan *connectors.Result, 64)
		done    = make(chan error, 1)
	)
	r.setInner(results)
	go func() { done <- imp.Import(ctx, records, results) }()

	var ferr error
	for record := range records {
		if ferr != nil {
			continue
		}
		// the record for the container's own root, parent of /data
		if record.Pathname == "/" {
			continue
		}
		out := *record
		rel := strings.TrimPrefix(record.Pathname, dataPath)
		out.Pathname = path.Join(prefix, rel)
		if rel == "" {
			out.FileInfo.Lname = path.Base(prefix)
		}
		ferr = r.send(ctx, &out)
	}

	// no more records: the helper only needs the results to close the
	// readers it holds, which happens when it is stopped anyway.
	r.setInner(nil)
	close(results)
	if err := <-done; err != nil && ferr == nil {
		ferr = fmt.Errorf("helper importer: %w", err)
	}
	return ferr
}

// restoreHelper is the fs exporter inside a helper, fed one record at a
// time.
type restoreHelper struct {
	vol     string
	h       *helper
	conn    *grpc.ClientConn
	exp     exporter.Exporter
	records chan *connectors.Record
	results chan *connectors.Result
	done    chan error
}

func (d *Docker) restoreInto(ctx context.Context, vol string) (*restoreHelper, error) {
	if d.cur != nil {
		if d.cur.vol == vol {
			return d.cur, nil
		}
		if err := d.cur.finish(ctx); err != nil {
			d.cur = nil
			return nil, err
		}
		d.cur = nil
	}

	h, err := d.startHelper(ctx, vol, false)
	if err != nil {
		return nil, err
	}
	conn, err := h.dial()
	if err != nil {
		h.stop(ctx)
		return nil, fmt.Errorf("dial helper: %w", err)
	}
	exp, err := gexporter.NewExporter(ctx, conn, d.remoteOptions(), "fs", map[string]string{
		"location": "fs://" + dataPath,
	})
	if err != nil {
		conn.Close()
		h.stop(ctx)
		return nil, fmt.Errorf("instantiate the helper exporter: %w", err)
	}

	rh := &restoreHelper{
		vol:     vol,
		h:       h,
		conn:    conn,
		exp:     exp,
		records: make(chan *connectors.Record),
		results: make(chan *connectors.Result),
		done:    make(chan error, 1),
	}
	go func() { rh.done <- exp.Export(ctx, rh.records, rh.results) }()
	d.cur = rh
	return rh, nil
}

// put hands one record to the exporter and waits for its verdict.
func (rh *restoreHelper) put(ctx context.Context, rec *connectors.Record) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case rh.records <- rec:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res, ok := <-rh.results:
		if !ok {
			return fmt.Errorf("helper exporter closed early")
		}
		return res.Err
	}
}

func (rh *restoreHelper) finish(ctx context.Context) error {
	close(rh.records)
	// drain whatever the exporter still sends before it returns
	go func() {
		for range rh.results {
		}
	}()
	err := <-rh.done
	if cerr := rh.exp.Close(ctx); err == nil {
		err = cerr
	}
	rh.conn.Close()
	rh.h.stop(ctx)
	return err
}
