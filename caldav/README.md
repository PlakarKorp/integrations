# 📅 integration-caldav

`integration-caldav` is a Plakar plugin that lets you **import and export calendar events** from any [CalDAV](https://en.wikipedia.org/wiki/CalDAV)-compatible calendar server directly into a Plakar repository.

> 🔐 Events are synced securely via CalDAV and stored as structured `.ics` files, preserving all calendar metadata.

---

## ⚙️ Requirements

- [**Plakar**](https://github.com/politaire/plakar) with plugin support
- A valid CalDAV server URL (e.g, Nextcloud, or Fastmail)
- Your CalDAV **username and password** (or app-specific token, depending on provider)

---

## 📦 Installation

To install this plugin using Plakar’s internal package system:

```bash
plakar pkg install integration-caldav
```

You're now ready to sync calendar data!

---

## 🚀 Usage

### ⬇️ Import calendar events into Plakar

```bash
plakar source add mycaldav caldav://<url> username=<user> password=<pass>
plakar at /path/to/repo backup @mycaldav
```

This fetches all calendar events accessible via the CalDAV endpoint and stores them as `.ics` files in the repository.

### ⬆️ Export calendar events from Plakar

```bash
plakar destination add mycaldav caldav://<url> username=<user> password=<pass>
plakar at /path/to/repo restore @mycaldav
```

This pushes `.ics` calendar files previously stored in Plakar back to your CalDAV server.

---

## 📂 Backup Format

Calendar events are saved in standard **`.ics` (iCalendar)** format, including:

- Event title, start/end time, recurrence rules
- Attendees and organizer information
- Calendar and event metadata (UID, creation date, etc.)

---

## 🔄 Round-Trip Support

> ✅ Events imported from CalDAV can be exported back without data loss.

The format is preserved 1:1 across import/export operations to ensure full fidelity.

---

## 🛠️ Tips

- **Use app-specific passwords** for providers like iCloud or Fastmail that don’t allow regular account passwords.
- **Read-only mode?** If your account is restricted, export operations might fail — check permissions.
- **Filter support:** Currently, all accessible calendars are imported/exported. Per-calendar selection may be added later.

---

## 📬 Feedback

Spotted an issue or have a feature in mind?  
Open an issue on the Plakar repository or reach out to the team.
