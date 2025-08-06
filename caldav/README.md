
# CalDAV Integration

## Overview

**CalDAV** is a standard protocol for accessing and managing calendar data on a remote server.
It allows clients to read, write, and sync calendar events using HTTP-based requests. CalDAV is supported by many calendar providers, including Nextcloud, Fastmail, Google, and Apple.

This integration allows:

- Seamless backup of calendar events from CalDAV servers into a Plakar repository
- Direct restoration of `.ics` calendar files to remote CalDAV destinations
- Compatibility with any CalDAV-compliant server or service


--- 

## Configuration

The configuration parameters are as follows:

- `location` (required): The URL of the CalDAV server in the form `caldav://<host>[:<port>]`
- `username` (required for most providers): The username to authenticate as
- `password` (required for most providers): The password or app-specific token to authenticate with

For providers that require OAuth2 (e.g. Google, Microsoft, Apple), you must also supply:

- `name`: The provider name (e.g. google, microsoft, apple)
- `client_id`: Your OAuth2 client ID
- `client_secret`: Your OAuth2 client secret
- `service_scope`: Provider-specific OAuth2 scope

These values must be set in the plugin configuration when adding the source or destination.

## Examples

```bash
# configure a CalDAV source
$ plakar source add mycaldav caldav://cal.example.org username=<user> password=<pass>

# backup the source (import events)
$ plakar backup @mycaldav

# configure a CalDAV destination
$ plakar destination add mycaldav caldav://cal.example.org username=<user> password=<pass>

# restore the snapshot to the destination (export events)
$ plakar restore -to @mycaldav <snapid>
```
