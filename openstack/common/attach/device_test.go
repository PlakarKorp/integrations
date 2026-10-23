package attach

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDevices points DeviceDir at a directory of the test's own, and returns
// it.
func fakeDevices(t *testing.T) string {
	t.Helper()
	saved := DeviceDir
	DeviceDir = t.TempDir()
	t.Cleanup(func() { DeviceDir = saved })
	return DeviceDir
}

// The by-id link resolves to the device node, which udev links a little
// after Nova reports the volume in use.
func TestDeviceResolvesLink(t *testing.T) {
	dir := fakeDevices(t)
	node := filepath.Join(dir, "vdb")
	go func() {
		time.Sleep(2 * devicePoll)
		assert.NoError(t, os.WriteFile(node, []byte("disk"), 0o600))
		assert.NoError(t, os.Symlink(node, filepath.Join(dir, "virtio-vol-1")))
	}()

	device, err := Device(t.Context(), "vol-1")
	require.NoError(t, err)
	// The temporary directory may sit behind a symlink of its own (macOS's
	// /var), resolved along with the link.
	want, err := filepath.EvalSymlinks(node)
	require.NoError(t, err)
	assert.Equal(t, want, device)
}

func TestDeviceNeverTurnsUp(t *testing.T) {
	defer func(d time.Duration) { deviceTimeout = d }(deviceTimeout)
	deviceTimeout = 100 * time.Millisecond
	fakeDevices(t)

	_, err := Device(t.Context(), "vol-1")
	require.ErrorContains(t, err, `no device for volume "vol-1"`)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// A device the process may not read (plakar not running as root, nor in the
// disk group) is reported with the cure.
func TestDeviceUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything")
	}
	dir := fakeDevices(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "virtio-vol-1"), []byte("disk"), 0))

	_, err := Device(t.Context(), "vol-1")
	require.ErrorIs(t, err, os.ErrPermission)
	assert.ErrorContains(t, err, "reading a block device takes root")
}

// udev names the device after the disk serial, which Nova cuts to 20
// characters of the volume ID.
func TestDeviceNames(t *testing.T) {
	names := deviceNames("384ba87e-a23e-4965-b3be-8f180e8a8625")
	assert.Equal(t, []string{
		"virtio-384ba87e-a23e-4965-b",
		"scsi-0QEMU_QEMU_HARDDISK_384ba87e-a23e-4965-b",
		"scsi-SQEMU_QEMU_HARDDISK_384ba87e-a23e-4965-b",
	}, names)
	assert.Equal(t, "virtio-vol-1", deviceNames("vol-1")[0])
}
