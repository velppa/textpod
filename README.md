> [!IMPORTANT]
>
> This a fork of [freetonik/textpod](https://github.com/freetonik/textpod) modified to my liking.
> Details in [my public notes linked to Textpod](https://finita.myaddr.dev/notes?q=textpod.md).

# Textpod

Local, web-based note-taking app inspired by "One Big Text File"
idea. Short demo:

[![Textpod short demo video](https://img.youtube.com/vi/VAqJJxaJNVM/0.jpg)](https://www.youtube.com/watch?v=VAqJJxaJNVM)

- Single page with all notes and a simple entry form (Markdown)
- All notes are stored in a single `notes.md` file
- Search/filtering when you start typing with `/`
- Start a link with `+` and Textpod will save a local single-page copy
- File and image attachments

## Usage
Here's how I run Textpod:

```sh
;; Building
find src -name '*.rs' -o -name '*.html' | entr -n -r cargo install --path .

;; Starting
cd homeserver/textpod
textpod --token '*********' --base-path notes
```

Results are here – <https://finita.myaddr.dev/notes>.
