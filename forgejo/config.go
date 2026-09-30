package forgejo

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const defaultDumpType = "tar.gz"

type config struct {
	location string

	forgejoBin string
	workPath   string
	customPath string
	configPath string
	tempDir    string
	database   string
	dumpType   string

	targetDir string

	skipRepository     bool
	skipLog            bool
	skipCustomDir      bool
	skipLFSData        bool
	skipAttachmentData bool
	skipPackageData    bool
	skipIndex          bool
	skipRepoArchives   bool
}

func parseImporterConfig(values map[string]string) (config, error) {
	cfg := config{
		location:   values["location"],
		forgejoBin: "forgejo",
		dumpType:   defaultDumpType,
	}
	if cfg.location == "" {
		cfg.location = "forgejo://local"
	}
	if v := values["forgejo_bin"]; v != "" {
		cfg.forgejoBin = v
	}
	cfg.workPath = values["work_path"]
	cfg.customPath = values["custom_path"]
	cfg.configPath = values["config_path"]
	cfg.tempDir = values["temp_dir"]
	cfg.database = values["database"]
	if v := values["dump_type"]; v != "" {
		v = strings.ToLower(v)
		if !supportedDumpType(v) {
			return cfg, fmt.Errorf("unsupported dump_type %q", v)
		}
		cfg.dumpType = v
	}

	boolOptions := map[string]*bool{
		"skip_repository":      &cfg.skipRepository,
		"skip_log":             &cfg.skipLog,
		"skip_custom_dir":      &cfg.skipCustomDir,
		"skip_lfs_data":        &cfg.skipLFSData,
		"skip_attachment_data": &cfg.skipAttachmentData,
		"skip_package_data":    &cfg.skipPackageData,
		"skip_index":           &cfg.skipIndex,
		"skip_repo_archives":   &cfg.skipRepoArchives,
	}
	for key, dest := range boolOptions {
		if err := parseBoolOption(values, key, dest); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func parseExporterConfig(values map[string]string) (config, error) {
	cfg := config{
		location:  values["location"],
		targetDir: values["target_dir"],
	}
	if cfg.location == "" {
		cfg.location = "forgejo://local"
	}
	if cfg.targetDir == "" {
		cfg.targetDir = targetDirFromLocation(cfg.location)
	}
	if cfg.targetDir == "" {
		return cfg, fmt.Errorf("target_dir is required or location must be forgejo:///path")
	}
	return cfg, nil
}

func parseBoolOption(values map[string]string, key string, dest *bool) error {
	raw := values[key]
	if raw == "" {
		return nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("invalid value for %s: %w", key, err)
	}
	*dest = parsed
	return nil
}

func targetDirFromLocation(location string) string {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "forgejo" {
		return ""
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return parsed.Path
	}
	if parsed.Opaque != "" {
		return parsed.Opaque
	}
	return ""
}

func supportedDumpType(value string) bool {
	switch strings.ToLower(value) {
	case "tar", "tar.gz", "tgz", "zip":
		return true
	default:
		return false
	}
}

func archiveName(dumpType string) string {
	switch strings.ToLower(dumpType) {
	case "tar":
		return "forgejo-dump.tar"
	case "zip":
		return "forgejo-dump.zip"
	case "tgz":
		return "forgejo-dump.tgz"
	default:
		return "forgejo-dump.tar.gz"
	}
}
