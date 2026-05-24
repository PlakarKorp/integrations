/*
 * Copyright (c) 2026 Gilles DUBOIS
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

package proxmox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const qemuConfigDir = "/etc/pve/qemu-server"
const lxcConfigDir = "/etc/pve/lxc"

func (c *Client) BackupVM(ctx context.Context, vmid int) (string, error) {
	args := []string{strconv.Itoa(vmid), "--dumpdir", c.cfg.DumpDir, "--mode", c.cfg.BackupMode, "--compress", c.cfg.BackupCompression}
	if c.cfg.Node != "" {
		args = append(args, "--node", c.cfg.Node)
	}

	stdout, stderr, err := c.runner.Run(ctx, "vzdump", args...)
	if err != nil {
		return "", fmt.Errorf("vzdump failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	archive := parseArchivePath(stdout + "\n" + stderr)
	if archive != "" {
		return archive, nil
	}

	fallback, err := c.findLatestDump(ctx, vmid)
	if err != nil {
		return "", err
	}
	if fallback == "" {
		return "", fmt.Errorf("unable to determine vzdump output file")
	}
	return fallback, nil
}

func (c *Client) BackupVMStream(ctx context.Context, vmid int) (string, io.ReadCloser, *int64, error) {
	vmType, err := c.VMType(ctx, vmid)
	if err != nil {
		return "", nil, nil, err
	}

	baseExt, err := dumpBaseExtension(vmType)
	if err != nil {
		return "", nil, nil, err
	}

	args := []string{strconv.Itoa(vmid), "--stdout", "--mode", c.cfg.BackupMode, "--compress", c.cfg.BackupCompression}
	if c.cfg.Node != "" {
		args = append(args, "--node", c.cfg.Node)
	}

	stream, err := c.runner.Stream(ctx, "vzdump", args...)
	if err != nil {
		return "", nil, nil, fmt.Errorf("vzdump stream failed: %w", err)
	}

	stderrBuf := &bytes.Buffer{}
	doneCh := make(chan struct{})

	go func() {
		defer close(doneCh)
		_, _ = io.Copy(stderrBuf, stream.Stderr)
	}()

	header, err := readStreamHeader(stream.Stdout, 16)
	if err != nil {
		_ = stream.Abort()
		_ = stream.Finish()
		<-doneCh
		return "", nil, nil, fmt.Errorf("unable to read vzdump stream header: %w: %s", err, strings.TrimSpace(stderrBuf.String()))
	}
	if len(header) == 0 {
		_ = stream.Abort()
		_ = stream.Finish()
		<-doneCh
		return "", nil, nil, fmt.Errorf("empty vzdump stream header: %s", strings.TrimSpace(stderrBuf.String()))
	}

	compressionSuffix := detectCompressionSuffix(header)
	timestamp := time.Now().Format("2006_01_02-15_04_05")
	archivePath := BuildDumpFilename(c.cfg, vmType, vmid, timestamp, baseExt, compressionSuffix)

	stdout := io.MultiReader(bytes.NewReader(header), stream.Stdout)

	size := int64(0)
	reader := &countingReadCloser{
		count: &size,
		reader: &streamReadCloser{
			stdout:     stdout,
			finish:     stream.Finish,
			stderr:     stderrBuf,
			stderrDone: doneCh,
		},
	}

	return archivePath, reader, &size, nil
}

func (c *Client) ReadQEMUConfig(ctx context.Context, vmid int) ([]byte, error) {
	return c.readVMConfig(ctx, "qemu", vmid)
}

func (c *Client) ReadLXCConfig(ctx context.Context, vmid int) ([]byte, error) {
	return c.readVMConfig(ctx, "lxc", vmid)
}

func VMConfigPath(vmType string, vmid int) (string, error) {
	switch vmType {
	case "qemu":
		return path.Join(qemuConfigDir, fmt.Sprintf("%d.conf", vmid)), nil
	case "lxc":
		return path.Join(lxcConfigDir, fmt.Sprintf("%d.conf", vmid)), nil
	default:
		return "", fmt.Errorf("unsupported VM type for config path: %s", vmType)
	}
}

func (c *Client) readVMConfig(ctx context.Context, vmType string, vmid int) ([]byte, error) {
	configPath, err := VMConfigPath(vmType, vmid)
	if err != nil {
		return nil, err
	}

	reader, err := c.Open(ctx, configPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read %s config %s: %w", vmType, configPath, err)
	}
	defer reader.Close()

	configData, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("unable to read %s config content %s: %w", vmType, configPath, err)
	}

	return configData, nil
}

var archiveRegex = regexp.MustCompile(`(?m)creating (?:vzdump )?archive ['"]([^'"]+)['"]`)

func parseArchivePath(output string) string {
	matches := archiveRegex.FindStringSubmatch(output)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func dumpBaseExtension(vmType string) (string, error) {
	switch vmType {
	case "qemu":
		return "vma", nil
	case "lxc":
		return "tar", nil
	default:
		return "", fmt.Errorf("unsupported VM type: %s", vmType)
	}
}

func readStreamHeader(reader io.Reader, size int) ([]byte, error) {
	if size <= 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	n, err := io.ReadFull(reader, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return buf[:n], nil
	}
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func detectCompressionSuffix(header []byte) string {
	if len(header) >= 2 && header[0] == 0x1f && header[1] == 0x8b {
		return ".gz"
	}
	if len(header) >= 4 && header[0] == 0x28 && header[1] == 0xb5 && header[2] == 0x2f && header[3] == 0xfd {
		return ".zst"
	}
	lzoMagic := []byte{0x89, 0x4c, 0x5a, 0x4f, 0x00, 0x0d, 0x0a, 0x1a, 0x0a}
	if len(header) >= len(lzoMagic) && bytes.HasPrefix(header, lzoMagic) {
		return ".lzo"
	}
	return ""
}

type streamReadCloser struct {
	stdout     io.Reader
	finish     func() error
	stderr     *bytes.Buffer
	stderrDone <-chan struct{}
	closed     bool
	finished   bool
	finishErr  error
}

func (r *streamReadCloser) Read(p []byte) (int, error) {
	n, err := r.stdout.Read(p)
	if err == io.EOF {
		if finishErr := r.finalize(); finishErr != nil {
			return n, finishErr
		}
	}
	return n, err
}

func (r *streamReadCloser) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.finalize()
}

func (r *streamReadCloser) finalize() error {
	if r.finished {
		return r.finishErr
	}
	r.finished = true

	var err error
	if r.finish != nil {
		err = r.finish()
	}
	if r.stderrDone != nil {
		<-r.stderrDone
	}
	if err != nil {
		r.finishErr = fmt.Errorf("vzdump failed: %w: %s", err, strings.TrimSpace(r.stderr.String()))
	}
	return r.finishErr
}

type countingReadCloser struct {
	reader io.ReadCloser
	count  *int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if c.count != nil && n > 0 {
		*c.count += int64(n)
	}
	return n, err
}

func (c *countingReadCloser) Close() error {
	return c.reader.Close()
}

func (c *Client) findLatestDump(ctx context.Context, vmid int) (string, error) {
	stdout, stderr, err := c.runner.Run(ctx, "ls", "-1", "--", c.cfg.DumpDir)
	if err != nil {
		return "", fmt.Errorf("fallback listing failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	var (
		bestPath string
		bestTime time.Time
	)

	for _, name := range strings.Split(strings.TrimSpace(stdout), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !isArchiveForVM(name, vmid) {
			continue
		}

		fullPath := path.Join(c.cfg.DumpDir, name)
		info, err := c.runner.Stat(ctx, fullPath)
		if err != nil {
			continue
		}
		modTime := info.ModTime()
		if bestPath == "" || modTime.After(bestTime) {
			bestPath = fullPath
			bestTime = modTime
		}
	}

	return bestPath, nil
}
