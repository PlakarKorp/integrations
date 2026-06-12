# webdav integration

This integration allow [plakar][plakar] to backup and restore WebDAV
remotes.

[plakar]: https://plakar.io/


## Installation

To install the webdav connector:

	$ plakar pkg add webdav


## Configuration

- `username`: optional user name
- `password`: optional password, used only if `username` is defined
- `insecure`: set to `true` to use the unencrypted `dav://` protocol


## Protocols

This integrations supports the unencrypted `dav://` and TLS-encrypted
`davs://` protocols.  `davs://` is reccomended.

To use the `dav://` protocol, the `insecure` options must be turned on.


## Examples

This example shows how to back up the data of a nextcloud user.  First,
define a `@nextcloud` source:

	$ plakar source add nextcloud davs://<nextcloud-url>/remote.php/dav/files/<your-user>
	$ plakar source set nextcloud username=your-username password=your-password

Then, back it up:

	$ plakar backup @nextcloud
