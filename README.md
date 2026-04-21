> [!IMPORTANT]
>
> This is a fork of [freetonik/textpod](https://github.com/freetonik/textpod), originally written in Rust and rewritten in Go, modified to my liking.
> Details in [my public notes linked to Textpod](https://finita.myaddr.dev/notes?q=textpod.md).

# Textpod

Local, web-based note-taking app inspired by "One Big Text File"
idea. Short demo:

[![Textpod short demo video](https://img.youtube.com/vi/VAqJJxaJNVM/0.jpg)](https://www.youtube.com/watch?v=VAqJJxaJNVM)

- Single page with all notes and a simple entry form (Markdown)
- All notes are stored in a single `notes.md` file
- Search/filtering when you start typing with `/`
- Start a link with `+` and Textpod will save a local single-page copy (via [`monolith`](https://github.com/Y2Z/monolith))
- File and image attachments
- Optional `--token` flag for read-only public access with authenticated writes
- `--base-path` for serving behind a reverse proxy under a sub-path
- Auto-reload when `notes.md` is changed externally
- `:tag:` syntax turned into clickable filter links
- `+url` and `+[label](url)` snapshot syntax for archiving pages

## Build

Requires Go 1.22+ (uses `net/http` method-prefixed routes).

```sh
go install github.com/velppa/textpod@latest
```

Or from a local checkout:

```sh
go build -o textpod .
```

## Usage

```sh
cd homeserver/textpod
textpod --token '*********' --base-path /notes
```

Flags:

- `--base-directory DIR` – `chdir` to `DIR` before starting
- `--port` – port number (default `3000`)
- `--listen` – listen address (default `127.0.0.1`)
- `--notes-file` – notes file (default `notes.md`)
- `--token` – require token for writes; without one, the UI is read-only
- `--base-path` – URL prefix when served behind a reverse proxy (e.g. `/notes`)

Results are here – <https://finita.myaddr.dev/notes>.
