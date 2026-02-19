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
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DumpFilenameVersion = 1
const QEMUConfigSidecarSuffix = "_qemu.conf"
const LXCConfigSidecarSuffix = "_lxc.conf"

var dumpNameRegex = regexp.MustCompile(`^vzdump(?:-v(\d+))?-(qemu|lxc)-(\d+)-`)

var archiveNameTemplate = `^vzdump(?:-v\d+)?-(qemu|lxc)-%d-.*\.(vma|tar)(\..+)?$`
var archiveSuffixRegex = regexp.MustCompile(`^\.(vma|tar)(\.[a-z0-9]+)?$`)

func ParseDumpFilename(name string) (string, int, error) {
	base := filepath.Base(name)
	matches := dumpNameRegex.FindStringSubmatch(base)
	if len(matches) != 4 {
		return "", 0, fmt.Errorf("invalid vzdump filename: %s", base)
	}

	versionStr := matches[1]
	if versionStr != "" {
		version, err := strconv.Atoi(versionStr)
		if err != nil {
			return "", 0, fmt.Errorf("invalid vzdump filename version: %s", base)
		}
		if version != DumpFilenameVersion {
			return "", 0, fmt.Errorf("unsupported vzdump filename version: %d", version)
		}
	}

	vmid, err := strconv.Atoi(matches[3])
	if err != nil {
		return "", 0, fmt.Errorf("invalid vmid in filename: %s", base)
	}
	return matches[2], vmid, nil
}

func isArchiveForVM(name string, vmid int) bool {
	pattern := fmt.Sprintf(archiveNameTemplate, vmid)
	re := regexp.MustCompile(pattern)
	return re.MatchString(name)
}

func BuildDumpFilename(_ *Config, vmType string, vmid int, timestamp, baseExt, compressionSuffix string) string {
	return fmt.Sprintf("vzdump-%s-%d-%s.%s%s", vmType, vmid, timestamp, baseExt, compressionSuffix)
}

func BuildRestoreDumpFilename(originalName, vmType string, vmid int, now time.Time) string {
	suffix := canonicalArchiveSuffix(originalName, vmType)
	return fmt.Sprintf("vzdump-%s-%d-%s%s", vmType, vmid, now.Format("2006_01_02-15_04_05"), suffix)
}

func BuildQEMUConfigSidecarFilename(archiveName string) string {
	return archiveName + QEMUConfigSidecarSuffix
}

func BuildLXCConfigSidecarFilename(archiveName string) string {
	return archiveName + LXCConfigSidecarSuffix
}

func IsQEMUConfigSidecarFilename(name string) bool {
	return strings.HasSuffix(strings.ToLower(filepath.Base(name)), QEMUConfigSidecarSuffix)
}

func IsLXCConfigSidecarFilename(name string) bool {
	return strings.HasSuffix(strings.ToLower(filepath.Base(name)), LXCConfigSidecarSuffix)
}

func IsConfigSidecarFilename(name string) bool {
	return IsQEMUConfigSidecarFilename(name) || IsLXCConfigSidecarFilename(name)
}

func canonicalArchiveSuffix(originalName, vmType string) string {
	baseExt := ".vma"
	if vmType == "lxc" {
		baseExt = ".tar"
	}

	baseName := filepath.Base(originalName)
	lowerName := strings.ToLower(baseName)
	idx := strings.Index(lowerName, baseExt)
	if idx < 0 {
		return baseExt
	}

	suffix := baseName[idx:]
	if archiveSuffixRegex.MatchString(strings.ToLower(suffix)) {
		return suffix
	}
	return baseExt
}
