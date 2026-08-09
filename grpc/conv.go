package integration_grpc

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/objects"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	ErrInvalidRecord = errors.New("invalid record")
)

func RecordToProto(record *connectors.Record) *Record {
	var errstr string
	if record.Err != nil {
		errstr = record.Err.Error()
	}

	var finfo *FileInfo
	if record.Err == nil && !record.IsXattr {
		fi := record.FileInfo
		finfo = &FileInfo{
			Name:      fi.Lname,
			Size:      fi.Lsize,
			Mode:      uint32(fi.Lmode),
			ModTime:   timestamppb.New(fi.LmodTime),
			Dev:       fi.Ldev,
			Ino:       fi.Lino,
			Uid:       fi.Luid,
			Gid:       fi.Lgid,
			Nlink:     uint32(fi.Lnlink),
			Username:  fi.Lusername,
			Groupname: fi.Lgroupname,
			Flags:     fi.Flags,
		}
	}

	hasreader := record.Err == nil && (record.IsXattr || record.FileInfo.Lmode.IsRegular())
	if record.Reader == nil {
		hasreader = false
	}

	return &Record{
		HasReader:          hasreader,
		Pathname:           record.Pathname,
		Error:              errstr,
		IsXattr:            record.IsXattr,
		XattrName:          record.XattrName,
		XattrType:          ExtendedAttributeType(record.XattrType),
		Target:             record.Target,
		FileInfo:           finfo,
		ExtendedAttributes: record.ExtendedAttributes,
		FileAttributes:     record.FileAttributes,
	}
}

func RecordFromProto(record *Record) (*connectors.Record, error) {
	var err error
	if record.Error != "" {
		err = fmt.Errorf("%s", record.Error)
	}

	var finfo objects.FileInfo

	if record.Error == "" && !record.IsXattr {
		fi := record.FileInfo

		if fi == nil {
			return nil, fmt.Errorf("%w %s: missing fileinfo", ErrInvalidRecord,
				record.Pathname)
		}

		finfo = objects.FileInfo{
			Lname:      fi.Name,
			Lsize:      fi.Size,
			Lmode:      fs.FileMode(fi.Mode),
			LmodTime:   fi.ModTime.AsTime(),
			Ldev:       fi.Dev,
			Lino:       fi.Ino,
			Luid:       fi.Uid,
			Lgid:       fi.Gid,
			Lnlink:     uint16(fi.Nlink),
			Lusername:  fi.Username,
			Lgroupname: fi.Groupname,
			Flags:      fi.Flags,
		}
	}

	return &connectors.Record{
		Pathname:           record.Pathname,
		Err:                err,
		IsXattr:            record.IsXattr,
		XattrName:          record.XattrName,
		XattrType:          objects.Attribute(record.XattrType),
		Target:             record.Target,
		FileInfo:           finfo,
		ExtendedAttributes: record.ExtendedAttributes,
		FileAttributes:     record.FileAttributes,
	}, nil
}

func ResultToProto(result *connectors.Result) *Result {
	var errstr string
	if result.Err != nil {
		errstr = result.Err.Error()
	}

	return &Result{
		Header: RecordToProto(&result.Record),
		Error:  errstr,
	}
}

func ResultFromProto(result *Result) (*connectors.Result, error) {
	record, err := RecordFromProto(result.Header)
	if err != nil {
		return nil, err
	}

	err = nil
	if result.Error != "" {
		err = fmt.Errorf("%s", result.Error)
	}

	return &connectors.Result{
		Record: *record,
		Err:    err,
	}, nil
}
