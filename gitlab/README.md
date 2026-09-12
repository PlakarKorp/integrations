# GitLab CE Integration for Plakar

Backup and restore a self-hosted GitLab CE instance with GitLab's native
`gitlab-backup` tooling.

The importer invokes `gitlab-backup create`, ingests the newest generated
`*_gitlab_backup.tar` archive, and includes GitLab configuration files. The
exporter writes those files to a target instance and invokes
`gitlab-backup restore`.

## Prerequisites

- Plakar >= 1.1
- GitLab CE with `gitlab-backup` available on the source and restore target
- Permission to read the GitLab backup directory and config files
- For remote operation: SSH access to the GitLab host

Use `use_sudo=true` when the account needs non-interactive sudo privileges for
GitLab backup commands or protected config paths.

## Configuration

| Option | Default | Description |
| --- | --- | --- |
| `location` | `gitlab://local` | GitLab source or target URI. |
| `backup_path` | `/var/opt/gitlab/backups` | Directory containing GitLab backup archives. |
| `config_paths` | `/etc/gitlab/gitlab.rb,/etc/gitlab/gitlab-secrets.json` | Comma-separated config files to include during backup. |
| `config_dir` | `/etc/gitlab` | Config restore directory. |
| `gitlab_backup_bin` | `gitlab-backup` | Backup CLI binary name or path. |
| `use_sudo` | `false` | Prefix privileged operations with `sudo -n`. |
| `ssh_host` | unset | Remote host. When set, the integration runs through SSH. |
| `ssh_user` | unset | SSH username. |
| `ssh_port` | unset | SSH port. |
| `ssh_identity_file` | unset | SSH private key path. |
| `ssh_bin` | `ssh` | SSH binary name or path. |
| `force` | `false` | Exporter only: pass `force=yes` to `gitlab-backup restore`. |

## Examples

Back up a local GitLab instance:

```sh
plakar backup gitlab://local use_sudo=true
```

Back up a remote GitLab instance over SSH:

```sh
plakar backup gitlab://gitlab.example.com \
  ssh_host=gitlab.example.com \
  ssh_user=git \
  use_sudo=true
```

Restore to a local GitLab instance:

```sh
plakar restore -to gitlab://local use_sudo=true <snapshot-id>
```

Restore to a remote GitLab host:

```sh
plakar restore -to gitlab://gitlab.example.com \
  ssh_host=gitlab.example.com \
  ssh_user=git \
  use_sudo=true \
  <snapshot-id>
```

## Restore Notes

GitLab restore has operational prerequisites outside Plakar, including stopping
services that write to the database before running restore and running GitLab's
post-restore checks afterward. Follow the GitLab backup and restore
documentation for the target version.
