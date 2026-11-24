/*
 * Copyright (c) 2026 Gilles Chehade <gilles@poolp.org>
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

package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
)

const (
	// Files above singlePartMax go through the multi-part flow, in
	// partSize chunks (the API wants 5 to 20 MiB per part).
	singlePartMax = 20 << 20
	partSize      = 10 << 20
	maxParts      = 1000
)

// Upload stores a file in Notion and returns the id of the file upload,
// ready to be attached to a block, an icon, a cover, a files property or
// a comment.  size must be the exact number of bytes r will yield.
func (c *Client) Upload(ctx context.Context, filename, contentType string, size int64, r io.Reader) (string, error) {
	if size < 0 {
		return "", fmt.Errorf("upload %s: unknown size", filename)
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(filename))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	if size <= singlePartMax {
		id, err := c.createUpload(ctx, map[string]any{
			"mode":         "single_part",
			"filename":     filename,
			"content_type": contentType,
		})
		if err != nil {
			return "", err
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(r, data); err != nil {
			return "", fmt.Errorf("upload %s: %w", filename, err)
		}
		if err := c.sendPart(ctx, id, filename, contentType, 0, bytes.NewReader(data)); err != nil {
			return "", err
		}
		return id, nil
	}

	parts := int((size + partSize - 1) / partSize)
	if parts > maxParts {
		return "", fmt.Errorf("upload %s: %d bytes need %d parts, the API allows %d", filename, size, parts, maxParts)
	}
	id, err := c.createUpload(ctx, map[string]any{
		"mode":            "multi_part",
		"filename":        filename,
		"content_type":    contentType,
		"number_of_parts": parts,
	})
	if err != nil {
		return "", err
	}
	buf := make([]byte, partSize)
	for n := 1; n <= parts; n++ {
		want := partSize
		if n == parts {
			want = int(size - int64(n-1)*partSize)
		}
		if _, err := io.ReadFull(r, buf[:want]); err != nil {
			return "", fmt.Errorf("upload %s: part %d: %w", filename, n, err)
		}
		if err := c.sendPart(ctx, id, filename, contentType, n, bytes.NewReader(buf[:want])); err != nil {
			return "", err
		}
	}
	var fu struct {
		Status string `json:"status"`
	}
	if err := c.send(ctx, http.MethodPost, "/v1/file_uploads/"+url.PathEscape(id)+"/complete", map[string]any{}, &fu); err != nil {
		return "", fmt.Errorf("upload %s: complete: %w", filename, err)
	}
	if fu.Status != "" && fu.Status != "uploaded" {
		return "", fmt.Errorf("upload %s: unexpected status %q after completion", filename, fu.Status)
	}
	return id, nil
}

func (c *Client) createUpload(ctx context.Context, body map[string]any) (string, error) {
	var fu struct {
		ID string `json:"id"`
	}
	if err := c.send(ctx, http.MethodPost, "/v1/file_uploads", body, &fu); err != nil {
		return "", fmt.Errorf("upload %v: %w", body["filename"], err)
	}
	if fu.ID == "" {
		return "", fmt.Errorf("upload %v: no id in response", body["filename"])
	}
	return fu.ID, nil
}

// sendPart posts one part (part 0 means single-part).  A part is read
// into memory so that a retried request can replay it.
func (c *Client) sendPart(ctx context.Context, id, filename, contentType string, part int, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("upload %s: reading part %d: %w", filename, part, err)
	}
	form := func() (io.Reader, string, error) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		if part > 0 {
			if err := w.WriteField("part_number", strconv.Itoa(part)); err != nil {
				return nil, "", err
			}
		}
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeQuotes(filename))}
		h["Content-Type"] = []string{contentType}
		fw, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := fw.Write(data); err != nil {
			return nil, "", err
		}
		if err := w.Close(); err != nil {
			return nil, "", err
		}
		return &b, w.FormDataContentType(), nil
	}
	var fu struct {
		Status string `json:"status"`
	}
	req := request{
		method:     http.MethodPost,
		path:       "/v1/file_uploads/" + url.PathEscape(id) + "/send",
		form:       form,
		idempotent: true, // re-sending the same part is harmless
	}
	if err := c.do(ctx, req, &fu); err != nil {
		return fmt.Errorf("upload %s: sending part %d: %w", filename, part, err)
	}
	if part == 0 && fu.Status != "" && fu.Status != "uploaded" {
		return fmt.Errorf("upload %s: unexpected status %q", filename, fu.Status)
	}
	return nil
}

func escapeQuotes(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\r', '\n':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FileUploadRef is the JSON that attaches an upload to an object.
func FileUploadRef(id string) map[string]any {
	return map[string]any{
		"type":        "file_upload",
		"file_upload": map[string]any{"id": id},
	}
}
