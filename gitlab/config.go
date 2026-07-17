package gitlab

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	DefaultBackupPath = "/var/opt/gitlab/backups"
	DefaultConfigDir  = "/etc/gitlab"
	DefaultBackupBin  = "gitlab-backup"
)

var DefaultConfigPaths = []string{
	"/etc/gitlab/gitlab.rb",
	"/etc/gitlab/gitlab-secrets.json",
}

type Config struct {
	Proto        string
	Location     string
	BackupPath   string
	ConfigPaths  []string
	ConfigDir    string
	BackupBin    string
	UseSudo      bool
	SSHHost      string
	SSHUser      string
	SSHPort      string
	SSHIdentity  string
	SSHBin       string
	RestoreForce bool
}

func NewConfig(proto string, config map[string]string) (Config, error) {
	cfg := Config{
		Proto:       proto,
		Location:    config["location"],
		BackupPath:  valueOrDefault(config["backup_path"], DefaultBackupPath),
		ConfigPaths: DefaultConfigPaths,
		ConfigDir:   valueOrDefault(config["config_dir"], DefaultConfigDir),
		BackupBin:   valueOrDefault(config["gitlab_backup_bin"], DefaultBackupBin),
		SSHHost:     config["ssh_host"],
		SSHUser:     config["ssh_user"],
		SSHPort:     config["ssh_port"],
		SSHIdentity: config["ssh_identity_file"],
		SSHBin:      valueOrDefault(config["ssh_bin"], "ssh"),
	}

	if v := strings.TrimSpace(config["config_paths"]); v != "" {
		cfg.ConfigPaths = splitList(v)
	}

	var err error
	if cfg.UseSudo, err = parseBool(config["use_sudo"], false); err != nil {
		return cfg, fmt.Errorf("invalid use_sudo: %w", err)
	}
	if cfg.RestoreForce, err = parseBool(config["force"], false); err != nil {
		return cfg, fmt.Errorf("invalid force: %w", err)
	}
	if cfg.SSHPort != "" {
		if _, err := strconv.Atoi(cfg.SSHPort); err != nil {
			return cfg, fmt.Errorf("invalid ssh_port: %w", err)
		}
	}
	if cfg.BackupPath == "" {
		return cfg, fmt.Errorf("backup_path cannot be empty")
	}
	if cfg.ConfigDir == "" {
		return cfg, fmt.Errorf("config_dir cannot be empty")
	}
	return cfg, nil
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseBool(value string, fallback bool) (bool, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return strconv.ParseBool(value)
}

func (c Config) remote() bool {
	return c.SSHHost != ""
}

func (c Config) Origin() string {
	if c.remote() {
		if c.SSHUser != "" {
			return c.Proto + "+ssh://" + c.SSHUser + "@" + c.SSHHost
		}
		return c.Proto + "+ssh://" + c.SSHHost
	}
	return c.Proto + "://local"
}

func (c Config) Ping(ctx context.Context) error {
	if c.remote() {
		_, err := c.runRemote(ctx, "command -v "+shellQuote(c.BackupBin)+" >/dev/null")
		return err
	}
	_, err := exec.LookPath(c.BackupBin)
	return err
}

func (c Config) CreateBackup(ctx context.Context) (string, error) {
	if c.remote() {
		create := shellJoin(c.commandArgs(c.BackupBin, "create"))
		script := "set -e\n" +
			create + " >/dev/null\n" +
			"ls -1t -- " + shellQuote(c.BackupPath) + "/*_gitlab_backup.tar | head -n 1"
		out, err := c.runRemote(ctx, script)
		if err != nil {
			return "", err
		}
		path := strings.TrimSpace(string(out))
		if path == "" {
			return "", fmt.Errorf("gitlab-backup create did not produce a backup path")
		}
		return path, nil
	}

	cmd := exec.CommandContext(ctx, c.commandArgs(c.BackupBin, "create")[0], c.commandArgs(c.BackupBin, "create")[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", commandError(err, out)
	}
	return newestBackup(c.BackupPath)
}

func newestBackup(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*_gitlab_backup.tar"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no GitLab backup tar found in %s", dir)
	}
	sort.Slice(matches, func(i, j int) bool {
		left, lerr := os.Stat(matches[i])
		right, rerr := os.Stat(matches[j])
		if lerr != nil || rerr != nil {
			return matches[i] > matches[j]
		}
		return left.ModTime().After(right.ModTime())
	})
	return matches[0], nil
}

func (c Config) OpenPath(ctx context.Context, path string) (io.ReadCloser, error) {
	if c.remote() {
		return c.remoteReader(ctx, shellJoin(c.commandArgs("cat", "--", path)))
	}
	if c.UseSudo {
		return c.localReader(ctx, c.commandArgs("cat", "--", path)...)
	}
	return os.Open(path)
}

func (c Config) PathExists(ctx context.Context, path string) bool {
	if c.remote() {
		_, err := c.runRemote(ctx, shellJoin(c.commandArgs("test", "-r", path)))
		return err == nil
	}
	_, err := os.Stat(path)
	return err == nil
}

func (c Config) WritePath(ctx context.Context, dst string, src io.Reader, mode os.FileMode) error {
	if c.remote() {
		dir := filepath.ToSlash(filepath.Dir(dst))
		if c.UseSudo {
			script := "set -e\n" + shellJoin(c.commandArgs("mkdir", "-p", "--", dir)) + "\n" +
				"sudo -n tee " + shellQuote(dst) + " >/dev/null"
			return c.runRemoteWithStdin(ctx, script, src)
		}
		script := "set -e\nmkdir -p -- " + shellQuote(dir) + "\ncat > " + shellQuote(dst)
		return c.runRemoteWithStdin(ctx, script, src)
	}
	if c.UseSudo {
		tmp, err := os.CreateTemp("", "plakar-gitlab-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if _, err := io.Copy(tmp, src); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if out, err := exec.CommandContext(ctx, "sudo", "-n", "install", "-m", fmt.Sprintf("%04o", mode.Perm()), "-D", tmpName, dst).CombinedOutput(); err != nil {
			return commandError(err, out)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

func (c Config) Restore(ctx context.Context, backupID string) error {
	if backupID == "" {
		return fmt.Errorf("missing GitLab backup id")
	}
	args := c.commandArgs(c.BackupBin, "restore", "BACKUP="+backupID)
	if c.RestoreForce {
		args = append(args, "force=yes")
	}
	if c.remote() {
		_, err := c.runRemote(ctx, shellJoin(args))
		return err
	}
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return commandError(err, out)
	}
	return nil
}

func (c Config) commandArgs(name string, args ...string) []string {
	out := make([]string, 0, len(args)+3)
	if c.UseSudo {
		out = append(out, "sudo", "-n")
	}
	out = append(out, name)
	out = append(out, args...)
	return out
}

func (c Config) sshArgs(script string) ([]string, error) {
	if c.SSHHost == "" {
		return nil, fmt.Errorf("ssh_host is required for remote operation")
	}
	args := []string{"-o", "BatchMode=yes"}
	if c.SSHPort != "" {
		args = append(args, "-p", c.SSHPort)
	}
	if c.SSHIdentity != "" {
		args = append(args, "-i", c.SSHIdentity)
	}
	target := c.SSHHost
	if c.SSHUser != "" {
		target = c.SSHUser + "@" + target
	}
	args = append(args, target, script)
	return args, nil
}

func (c Config) runRemote(ctx context.Context, script string) ([]byte, error) {
	args, err := c.sshArgs(script)
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, c.SSHBin, args...).CombinedOutput()
	if err != nil {
		return nil, commandError(err, out)
	}
	return out, nil
}

func (c Config) runRemoteWithStdin(ctx context.Context, script string, stdin io.Reader) error {
	args, err := c.sshArgs(script)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c.SSHBin, args...)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return commandError(err, out)
	}
	return nil
}

func (c Config) remoteReader(ctx context.Context, script string) (io.ReadCloser, error) {
	args, err := c.sshArgs(script)
	if err != nil {
		return nil, err
	}
	return startCommandReader(ctx, c.SSHBin, args...)
}

func (c Config) localReader(ctx context.Context, args ...string) (io.ReadCloser, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("missing command")
	}
	return startCommandReader(ctx, args[0], args[1:]...)
}

type commandReader struct {
	io.ReadCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func startCommandReader(ctx context.Context, name string, args ...string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &commandReader{ReadCloser: stdout, cmd: cmd, stderr: &stderr}, nil
}

func (r *commandReader) Close() error {
	_ = r.ReadCloser.Close()
	if err := r.cmd.Wait(); err != nil {
		return commandError(err, r.stderr.Bytes())
	}
	return nil
}

func commandError(err error, out []byte) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = shellQuote(arg)
	}
	return strings.Join(parts, " ")
}
