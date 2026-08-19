# routeros integration

This integration allows [plakar][plakar] to backup and restore
[routeros][routeros] appliance.  At the moment, it can backup both the
text export (a `.rsc` file) or a system backup, and restore only
`.rsc`.  Restoration of system backups in upcoming.

[plakar]:     https://plakar.io/
[kubernetes]: https://kubernetes.io/


## Configuration

All these parameters are optional.

- `user`: the username, taken from the location if not set.
- `password`: the password for ssh
- `private_key`: the private key to use for ssh
- `private_key_passphrase`: an eventual passphrase to unlock the private key.

Restore-specific options:

- `dry_run`: whether to attempt a "dry run" evaluation of the `.rsc`
  file.


## Examples

Backup a text export of the configuration applied:

	$ plakar backup routeros+export://admin@192.168.88.1

Backup the system configuration:

	$ plakar backup routeros+backup://admin@192.168.88.1

Restore (in dry-run mode) the text export previously backed up:

	$ plakar restore -to routeros://admin@192.168.88.1 -o dry_run=true abcd:
