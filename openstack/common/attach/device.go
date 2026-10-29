/*
 * Copyright (c) 2026 Peeyush Aggarwal <peeyushaggarwal94@gmail.com>
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

package attach

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// How the host's kernel names an attached volume.
var (
	// DeviceDir is where udev links block devices by hardware ID. A variable
	// so tests can point it at a directory of their own.
	DeviceDir = "/dev/disk/by-id"
	// deviceTimeout bounds the wait for the device once Nova reports the
	// volume in use; the kernel usually sees it within a second.
	deviceTimeout = time.Minute
	devicePoll    = 250 * time.Millisecond
)

// serialLen is how much of a volume's ID Nova's libvirt driver puts in the
// disk's serial number, which is what udev names the device after.
const serialLen = 20

// Device waits for the kernel to see the volume Nova just attached to the
// host, and returns the device node it turns up as, checked to be readable:
// that takes root or the disk group, and is better found out before plakar
// opens the record than when it does.
func DeviceReadable(ctx context.Context, volumeID string) (string, error) {
	return device(ctx, volumeID, os.O_RDONLY, "reading")
}

// DeviceWritable is DeviceReadable for a restore, which writes a raw disk's bytes
// to the volume: the device is checked to be writable instead. A qcow2 disk
// cannot be written to a block device, and is the caller's to refuse.
func DeviceWritable(ctx context.Context, volumeID string) (string, error) {
	return device(ctx, volumeID, os.O_WRONLY, "writing")
}

// device waits for the volume's device and opens it with flag once, to find
// out before any bytes move whether the process may. verb names the access
// in the permission error's cure.
func device(ctx context.Context, volumeID string, flag int, verb string) (string, error) {
	device, err := waitDevice(ctx, volumeID)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(device, flag, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return "", fmt.Errorf("%w (%s a block device takes root, or membership of the disk group)", err, verb)
		}
		return "", err
	}
	return device, f.Close()
}

// deviceNames are the by-id links udev makes for a volume, by disk bus: virtio-blk, then virtio-scsi.
// it cuts the volumeID to 20 characters to be used as the serial number.
func deviceNames(volumeID string) []string {
	serial := volumeID
	if len(serial) > serialLen {
		serial = serial[:serialLen]
	}
	return []string{
		"virtio-" + serial,
		"scsi-0QEMU_QEMU_HARDDISK_" + serial,
		"scsi-SQEMU_QEMU_HARDDISK_" + serial,
	}
}

// waitDevice waits for the volume's by-id link, and returns the device node
// it resolves to.
func waitDevice(ctx context.Context, volumeID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, deviceTimeout)
	defer cancel()
	names := deviceNames(volumeID)
	for {
		for _, name := range names {
			device, err := filepath.EvalSymlinks(filepath.Join(DeviceDir, name))
			if err == nil {
				return device, nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("resolve %s: %w", name, err)
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("no device for volume %q under %s: %w", volumeID, DeviceDir, ctx.Err())
		case <-time.After(devicePoll):
		}
	}
}
