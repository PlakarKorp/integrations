# 📦 integration-rclone

`integration-rclone` is a Plakar plugin that allows you to back up and restore your cloud data from various cloud providers supported by Rclone (like Google Drive, Google Photos, OneDrive, iCloud Drive, and more) directly into a Plakar repository.

> 🔐 All data is handled via Rclone and saved/restored seamlessly with Plakar’s Kloset immutable data store.

---

## ⚙️ Requirements

* [**Plakar**](https://github.com/PlakarKorp/plakar) installed with plugin support
* [**Rclone**](https://rclone.org/install/) installed on your system
* A configured **Rclone remote** for your cloud provider (e.g., Google Drive, OneDrive, iCloud Drive)
* Supported providers currently include: `google drive`, `google photos`, `onedrive`, `opendrive`, `iclouddrive` (excl. iCloud Photos), `dropbox`, and `protondrive`.

---

## 📦 Installation

Clone the plugin repository and build the plugin:

```bash
git clone git@github.com:PlakarKorp/integration-rclone.git
cd integration-rclone
make
```

Then, build the plugin package with Plakar:

```bash
plakar pkg build integration-rclone/manifest.yaml
```

Install the generated plugin package (`rclone-vx.x.x.ptar`):

```bash
plakar pkg install rclone-vx.x.x.ptar
```

You’re now ready to configure and use the Rclone integration.

---

## 🔧 Configuration

You must have a valid Rclone remote configured (`rclone config`) for your cloud provider. The remote config lives in `~/.config/rclone/rclone.conf`.

Example snippet for `rclone.conf`:

```ini
[myremote]
type = onedrive
token = {"..."}
drive_id = ...
drive_type = business
```

Translate this config to YAML in your Plakar configuration, adding the `location` field matching your provider:

```yaml
myremote:
  type: onedrive
  token: '{"..."}'
  drive_id: ...
  drive_type: business
  location: 'onedrive://'
```

Supported `location` values:

* `onedrive://`
* `opendrive://`
* `googledrive://`
* `iclouddrive://`
* `googlephotos://`
* `dropbox://`
* `protondrive://`

*Note:* iCloud Drive support excludes iCloud Photos at this time.

---

## 🚀 Usage

Back up your cloud data into a Kloset repository at `/path/to/backup`:

```bash
plakar at /path/to/backup create
plakar at /path/to/backup backup @myremote
```

Restore data from the Kloset repository back to your cloud provider:

```bash
plakar at /path/to/backup restore -to @myremote <snapshot_id> [path/to/file]
```

You can list snapshots with:

```bash
plakar at /path/to/backup ls
```

---

## 📂 Backup Format

Data backed up with Rclone integration includes:

* Files and folders as managed by the cloud provider
* Metadata required for restoration
* Support for partial or full restores

*Note:* Media-specific APIs (like iCloud Photos) are not fully supported yet.

---

## 🛠️ Tips

* Ensure your Rclone remote is correctly configured and authenticated before use.
* Keep your tokens and credentials secure.
* Currently, Plakar supports only a subset of Rclone’s providers. More will be added upon demand.

---

## 📬 Feedback

Encounter an issue or want a new provider supported? Please:

* [Open an issue on GitHub](https://github.com/PlakarKorp/plakar/issues/new?title=Rclone%20integration%20issue)
* Join our [Discord community](https://discord.gg/uuegtnF2Q5) to discuss and get real-time help.
