package gitlab

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewConfigDefaults(t *testing.T) {
	cfg, err := NewConfig("gitlab", map[string]string{"location": "gitlab://local"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BackupPath != DefaultBackupPath {
		t.Fatalf("BackupPath=%q, want %q", cfg.BackupPath, DefaultBackupPath)
	}
	if cfg.ConfigDir != DefaultConfigDir {
		t.Fatalf("ConfigDir=%q, want %q", cfg.ConfigDir, DefaultConfigDir)
	}
	if cfg.BackupBin != DefaultBackupBin {
		t.Fatalf("BackupBin=%q, want %q", cfg.BackupBin, DefaultBackupBin)
	}
	if cfg.remote() {
		t.Fatal("default config should be local")
	}
}

func TestNewConfigParsesSSHAndLists(t *testing.T) {
	cfg, err := NewConfig("gitlab", map[string]string{
		"ssh_host":     "gitlab.example.com",
		"ssh_user":     "git",
		"ssh_port":     "2222",
		"use_sudo":     "true",
		"config_paths": "/etc/gitlab/gitlab.rb, /etc/gitlab/gitlab-secrets.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.remote() {
		t.Fatal("expected remote config")
	}
	if !cfg.UseSudo {
		t.Fatal("expected sudo mode")
	}
	if len(cfg.ConfigPaths) != 2 {
		t.Fatalf("ConfigPaths length=%d, want 2", len(cfg.ConfigPaths))
	}
}

func TestShellQuote(t *testing.T) {
	got := shellQuote("a'b c")
	want := "'a'\"'\"'b c'"
	if got != want {
		t.Fatalf("shellQuote=%q, want %q", got, want)
	}
}

func TestCommandArgsWithSudo(t *testing.T) {
	cfg := Config{UseSudo: true}
	got := cfg.commandArgs("gitlab-backup", "restore", "BACKUP=abc")
	want := []string{"sudo", "-n", "gitlab-backup", "restore", "BACKUP=abc"}
	if len(got) != len(want) {
		t.Fatalf("command length=%d, want %d: %#v", len(got), len(want), got)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("command[%d]=%q, want %q", idx, got[idx], want[idx])
		}
	}
}

func TestSSHArgs(t *testing.T) {
	cfg := Config{SSHHost: "gitlab.example.com", SSHUser: "git", SSHPort: "2222", SSHIdentity: "/tmp/key"}
	got, err := cfg.sshArgs("true")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-o", "BatchMode=yes", "-p", "2222", "-i", "/tmp/key", "git@gitlab.example.com", "true"}
	if len(got) != len(want) {
		t.Fatalf("ssh args length=%d, want %d: %#v", len(got), len(want), got)
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("sshArgs[%d]=%q, want %q", idx, got[idx], want[idx])
		}
	}
}

func TestNewestBackup(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "1700000000_gitlab_backup.tar")
	newPath := filepath.Join(dir, "1800000000_gitlab_backup.tar")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	got, err := newestBackup(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != newPath {
		t.Fatalf("newestBackup=%q, want %q", got, newPath)
	}
}

func TestBackupIDFromFilename(t *testing.T) {
	result := backupIDFromArchiveName("/backup/1700000000_2026_06_02_18.11.0_gitlab_backup.tar")
	if result != "1700000000_2026_06_02_18.11.0" {
		t.Fatalf("backup id=%q", result)
	}
}
