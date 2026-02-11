package proxmox

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	MetadataFormat  = "proxmox-backup-meta"
	MetadataVersion = 1

	metadataPrefix = ".plakar-meta-"
	metadataSuffix = ".json"
)

type DumpMetadata struct {
	Format            string    `json:"format"`
	Version           int       `json:"version"`
	VMID              int       `json:"vmid"`
	VMType            string    `json:"vm_type,omitempty"`
	Node              string    `json:"node,omitempty"`
	BackupMode        string    `json:"backup_mode,omitempty"`
	BackupCompression string    `json:"backup_compression,omitempty"`
	ArchiveName       string    `json:"archive_name"`
	ArchiveSize       int64     `json:"archive_size,omitempty"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
	Origin            string    `json:"origin,omitempty"`
}

func MetadataFilename(archiveName string) string {
	return metadataPrefix + archiveName + metadataSuffix
}

func ParseMetadataFilename(name string) (string, bool) {
	if !strings.HasPrefix(name, metadataPrefix) || !strings.HasSuffix(name, metadataSuffix) {
		return "", false
	}
	archiveName := strings.TrimSuffix(strings.TrimPrefix(name, metadataPrefix), metadataSuffix)
	if archiveName == "" {
		return "", false
	}
	return archiveName, true
}

func NewDumpMetadata(cfg *Config, vmid int, archiveName string, info os.FileInfo) DumpMetadata {
	meta := DumpMetadata{
		Format:            MetadataFormat,
		Version:           MetadataVersion,
		VMID:              vmid,
		Node:              cfg.Node,
		BackupMode:        cfg.BackupMode,
		BackupCompression: cfg.BackupCompression,
		ArchiveName:       archiveName,
		Origin:            cfg.Origin(),
	}

	if info != nil {
		meta.ArchiveSize = info.Size()
		meta.CreatedAt = info.ModTime()
	} else {
		meta.CreatedAt = time.Now()
	}

	if vmType, _, err := ParseDumpFilename(archiveName); err == nil {
		meta.VMType = vmType
	}

	return meta
}

func EncodeDumpMetadata(meta DumpMetadata) ([]byte, error) {
	return json.Marshal(meta)
}

func DecodeDumpMetadata(reader io.Reader) (DumpMetadata, error) {
	payload, err := io.ReadAll(reader)
	if err != nil {
		return DumpMetadata{}, err
	}

	var meta DumpMetadata
	if err := json.Unmarshal(payload, &meta); err != nil {
		return DumpMetadata{}, err
	}

	if meta.Format != "" && meta.Format != MetadataFormat {
		return DumpMetadata{}, fmt.Errorf("unsupported metadata format: %s", meta.Format)
	}
	if meta.Version != 0 && meta.Version != MetadataVersion {
		return DumpMetadata{}, fmt.Errorf("unsupported metadata version: %d", meta.Version)
	}

	return meta, nil
}
