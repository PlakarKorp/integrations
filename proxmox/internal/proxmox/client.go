package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	cfg    *Config
	runner Runner
}

func NewClient(cfg *Config) (*Client, error) {
	runner, err := NewRunner(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, runner: runner}, nil
}

func (c *Client) Close() error {
	if c.runner != nil {
		return c.runner.Close()
	}
	return nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, stderr, err := c.runner.Run(ctx, "pvesh", "get", "/version", "--output-format", "json")
	if err != nil {
		return fmt.Errorf("pvesh unavailable: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

func (c *Client) BackupVM(ctx context.Context, vmid int) (string, error) {
	args := []string{strconv.Itoa(vmid), "--dumpdir", DefaultDumpDir, "--mode", c.cfg.BackupMode, "--compress", c.cfg.BackupCompression}
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

func (c *Client) ListAllVMIDs(ctx context.Context) ([]int, error) {
	stdout, stderr, err := c.runner.Run(ctx, "pvesh", "get", "/cluster/resources", "--type", "vm", "--output-format", "json")
	if err != nil {
		return nil, fmt.Errorf("pvesh get cluster resources failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	var resources []vmResource
	if err := json.Unmarshal([]byte(stdout), &resources); err != nil {
		return nil, fmt.Errorf("failed to parse cluster resources: %w", err)
	}
	return filterVMIDs(resources, c.cfg.Node), nil
}

func (c *Client) ListPoolVMIDs(ctx context.Context, pool string) ([]int, error) {
	stdout, stderr, err := c.runner.Run(ctx, "pvesh", "get", "/pools/"+pool, "--output-format", "json")
	if err != nil {
		return nil, fmt.Errorf("pvesh get pool failed: %w: %s", err, strings.TrimSpace(stderr))
	}

	var response poolResponse
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		return nil, fmt.Errorf("failed to parse pool response: %w", err)
	}
	return filterVMIDs(response.Members, c.cfg.Node), nil
}

func (c *Client) Open(ctx context.Context, filepath string) (io.ReadCloser, error) {
	return c.runner.Open(ctx, filepath)
}

func (c *Client) Create(ctx context.Context, filepath string) (io.WriteCloser, error) {
	return c.runner.Create(ctx, filepath)
}

func (c *Client) Stat(ctx context.Context, filepath string) (os.FileInfo, error) {
	return c.runner.Stat(ctx, filepath)
}

func (c *Client) Remove(ctx context.Context, filepath string) error {
	return c.runner.Remove(ctx, filepath)
}

func (c *Client) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	return c.runner.Run(ctx, name, args...)
}

type vmResource struct {
	VMID int    `json:"vmid"`
	Type string `json:"type"`
	Node string `json:"node"`
}

type poolResponse struct {
	Members []vmResource `json:"members"`
}

func filterVMIDs(resources []vmResource, node string) []int {
	set := make(map[int]struct{})
	for _, item := range resources {
		if item.Type != "qemu" && item.Type != "lxc" {
			continue
		}
		if node != "" && item.Node != node {
			continue
		}
		set[item.VMID] = struct{}{}
	}

	vmids := make([]int, 0, len(set))
	for vmid := range set {
		vmids = append(vmids, vmid)
	}
	sort.Ints(vmids)
	return vmids
}

var archiveRegex = regexp.MustCompile(`(?m)creating (?:vzdump )?archive ['"]([^'"]+)['"]`)

func parseArchivePath(output string) string {
	matches := archiveRegex.FindStringSubmatch(output)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func (c *Client) findLatestDump(ctx context.Context, vmid int) (string, error) {
	stdout, stderr, err := c.runner.Run(ctx, "ls", "-1", "--", DefaultDumpDir)
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

		fullPath := path.Join(DefaultDumpDir, name)
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

var dumpNameRegex = regexp.MustCompile(`^vzdump-(qemu|lxc)-(\d+)-`)

var archiveNameTemplate = `^vzdump-(qemu|lxc)-%d-.*\.(vma|tar)(\..+)?$`

func ParseDumpFilename(name string) (string, int, error) {
	base := filepath.Base(name)
	matches := dumpNameRegex.FindStringSubmatch(base)
	if len(matches) != 3 {
		return "", 0, fmt.Errorf("invalid vzdump filename: %s", base)
	}
	vmid, err := strconv.Atoi(matches[2])
	if err != nil {
		return "", 0, fmt.Errorf("invalid vmid in filename: %s", base)
	}
	return matches[1], vmid, nil
}

func isArchiveForVM(name string, vmid int) bool {
	pattern := fmt.Sprintf(archiveNameTemplate, vmid)
	re := regexp.MustCompile(pattern)
	return re.MatchString(name)
}
