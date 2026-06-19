package rclone

import (
	"context"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	rclonefs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
)

// objectinfo implements rclonefs.ObjectInfo
type objectinfo struct {
	record *connectors.Record
}

var _ rclonefs.ObjectInfo = &objectinfo{}

func (o *objectinfo) Fs() rclonefs.Info {
	return nil
}

func (o *objectinfo) String() string { return o.record.Pathname }
func (o *objectinfo) Remote() string { return o.record.Pathname }
func (o *objectinfo) Size() int64    { return o.record.FileInfo.Lsize }
func (o *objectinfo) Storable() bool { return true }

func (o *objectinfo) ModTime(context.Context) time.Time {
	return o.record.FileInfo.LmodTime
}

func (o *objectinfo) Hash(context.Context, hash.Type) (string, error) {
	return "", nil
}
