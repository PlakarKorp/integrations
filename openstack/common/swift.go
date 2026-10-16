package common

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	gcopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/imageimport"
	"github.com/gophercloud/gophercloud/v2/openstack/objectstorage/v1/containers"
	"github.com/gophercloud/gophercloud/v2/openstack/objectstorage/v1/objects"
	"golang.org/x/sync/errgroup"
)

// segmentSize and segmentWorkers bound both Swift's per-segment overhead and
// how much of the image sits in memory at once; variables so tests can
// shrink them.
var (
	segmentSize    = 128 << 20
	segmentWorkers = 4
)

const (
	// tempURLTTL covers Glance's web-download fetch of a multi-hundred-GB
	// image over the OpenStack internal network.
	tempURLTTL = 4 * 60 * 60

	manifestObject = "image"
	// segmentPrefix is the "prefix" of the manifest's container/prefix: Swift
	// rejects an empty one.
	segmentPrefix        = "seg-"
	stagedCleanupTimeout = 2 * time.Minute
)

func (c *Client) swift() (*gophercloud.ServiceClient, error) {
	return c.service("object storage", gcopenstack.NewObjectStorageV1)
}

// Each image gets its own pair of containers: sharing one fixed container
// across concurrent restores would mean sharing one Temp URL key, and
// resetting that key while another restore's import is still in flight
// invalidates its in-progress Temp URL.
func stagingContainer(imageID string) string  { return "plakar-restore-staging-" + imageID }
func segmentsContainer(imageID string) string { return stagingContainer(imageID) + "_segments" }

func segmentName(index int) string { return fmt.Sprintf("%s%08d", segmentPrefix, index) }

// uploadStagedImage stages r in Swift as a Dynamic Large Object, uploaded in
// parallel segments, then has Glance pull the assembled object server-side
// via web-download. This is the only way to parallelize an upload into
// Glance: gophercloud's imagedata API is a single full-body PUT, with no
// chunked or byte-range variant.
//
// The returned cleanup must run only once the caller's poll for the image to
// become active or error finishes: web-download is asynchronous, so Glance is
// still reading from the staged object after this returns.
func uploadStagedImage(ctx context.Context, swift, image *gophercloud.ServiceClient, imageID string, r io.Reader) (cleanup func(context.Context), err error) {
	staging, segments := stagingContainer(imageID), segmentsContainer(imageID)
	segmentCount := 0
	cleanup = func(ctx context.Context) { cleanupStaged(ctx, swift, staging, segments, segmentCount) }

	key, err := randomKey()
	if err != nil {
		return cleanup, fmt.Errorf("generate temp URL key: %w", err)
	}
	if _, err := containers.Create(ctx, swift, staging, containers.CreateOpts{TempURLKey: key}).Extract(); err != nil {
		return cleanup, fmt.Errorf("create staging container %q: %w", staging, err)
	}
	if _, err := containers.Create(ctx, swift, segments, nil).Extract(); err != nil {
		return cleanup, fmt.Errorf("create segments container %q: %w", segments, err)
	}

	segmentCount, err = uploadSegments(ctx, swift, segments, r)
	if err != nil {
		return cleanup, err
	}
	if segmentCount == 0 {
		return cleanup, errors.New("empty disk image")
	}

	if _, err := objects.Create(ctx, swift, staging, manifestObject, objects.CreateOpts{
		Content:        bytes.NewReader(nil),
		ObjectManifest: segments + "/" + segmentPrefix,
	}).Extract(); err != nil {
		return cleanup, fmt.Errorf("create manifest: %w", err)
	}

	tempURL, err := objects.CreateTempURL(ctx, swift, staging, manifestObject, objects.CreateTempURLOpts{
		Method:     objects.GET,
		TTL:        tempURLTTL,
		TempURLKey: key,
	})
	if err != nil {
		return cleanup, fmt.Errorf("create temp URL: %w", err)
	}

	if err := imageimport.Create(ctx, image, imageID, imageimport.CreateOpts{
		Name: imageimport.WebDownloadMethod,
		URI:  tempURL,
	}).ExtractErr(); err != nil {
		return cleanup, fmt.Errorf("import from staged object: %w", err)
	}

	return cleanup, nil
}

// uploadSegments reads r sequentially — an io.Reader cannot be read out of
// order — and dispatches each segment to a bounded worker pool, so the
// parallelism is in the concurrent PUTs rather than the read.
func uploadSegments(ctx context.Context, swift *gophercloud.ServiceClient, segments string, r io.Reader) (int, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(segmentWorkers)

	n := 0
	for {
		buf := make([]byte, segmentSize)
		read, err := io.ReadFull(r, buf)
		if read > 0 {
			index := n
			content := bytes.NewReader(buf[:read])
			g.Go(func() error {
				name := segmentName(index)
				if _, err := objects.Create(ctx, swift, segments, name, objects.CreateOpts{Content: content}).Extract(); err != nil {
					return fmt.Errorf("upload segment %q: %w", name, err)
				}
				return nil
			})
			n++
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			_ = g.Wait()
			return n, fmt.Errorf("read segment %d: %w", n, err)
		}
	}
	return n, g.Wait()
}

func randomKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// cleanupStaged deletes the staged segments, manifest and their containers.
// It is best-effort, logged rather than returned, as plakar already ignores
// the exporter's own Close errors.
func cleanupStaged(ctx context.Context, swift *gophercloud.ServiceClient, staging, segments string, segmentCount int) {
	ctx = context.WithoutCancel(ctx)
	ctx, cancel := context.WithTimeout(ctx, stagedCleanupTimeout)
	defer cancel()

	if segmentCount > 0 {
		names := make([]string, segmentCount)
		for i := range names {
			names[i] = segmentName(i)
		}
		if _, err := objects.BulkDelete(ctx, swift, segments, names).Extract(); err != nil {
			log.Printf("openstack: cleanup: delete %d staged segments in %q: %v", segmentCount, segments, err)
		}
	}
	if _, err := containers.Delete(ctx, swift, segments).Extract(); !isDeleted(err) {
		log.Printf("openstack: cleanup: delete container %q: %v", segments, err)
	}

	if _, err := objects.Delete(ctx, swift, staging, manifestObject, nil).Extract(); !isDeleted(err) {
		log.Printf("openstack: cleanup: delete manifest in %q: %v", staging, err)
	}
	if _, err := containers.Delete(ctx, swift, staging).Extract(); !isDeleted(err) {
		log.Printf("openstack: cleanup: delete container %q: %v", staging, err)
	}
}
