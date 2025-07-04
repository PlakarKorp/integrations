# 📦 integration-notion

`integration-notion` is a Plakar plugin that lets you back up your Notion pages or workspace directly into a Plakar repository.

> 🔐 All content is fetched via the Notion API and saved as structured JSON files, including both data and metadata.

---

## ⚙️ Requirements

- [**Plakar**](https://github.com/politaire/plakar) installed with plugin support
- A valid [**Notion API token**](https://www.notion.com/my-integrations) (`ntn_xxx`)  
- The target Notion pages must be **shared** with the integration you created
---

## 📦 Installation

This plugin is distributed via Plakar’s internal plugin system. Assuming you already have Plakar installed:

```bash
plakar pkg install integration-notion
```

That’s it — you’re ready to configure and use it.

---

## 🚀 Usage

To back up your Notion pages, run:

```bash
plakar at /path/to/repo backup notion:// token=<ntn_xxx>
```

Suppsing you have a Plakar repository at `/path/to/repo` and Replace `<ntn_xxx>` with your actual Notion API token.

---

## 📂 Backup Format

Backed-up content is stored as **JSON files**, including:
- Page content
- Metadata (title, ID, parent, etc.)
- Block structure and types

---

## 🔄 Restoration

> 🧪 Restoration is **not available yet**, but support is planned in a future release.

Stay tuned!

---

## 🛠️ Tips

- **Sharing:** Make sure your integration is shared with each Notion page you want to back up.  
  → Create an [Integration in Notion](https://www.notion.com/my-integrations) and share it with the pages you want to back up.

  → See [Notion’s guide on integrations](https://developers.notion.com/docs/getting-started#step-1-create-an-integration) for how to create and share your token properly.
- **Security:** Keep your token safe — don’t commit it into version control.
- **Selective backups:** Currently, the plugin pulls all shared pages — filtering support may come later.

## 📬 Feedback

Found a bug? Have a feature request? Open an issue or ping the Plakar team.
