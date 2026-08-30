# micawber

[![ci](https://github.com/shutx-net/micawber/actions/workflows/ci.yml/badge.svg)](https://github.com/shutx-net/micawber/actions/workflows/ci.yml)
[![go](https://img.shields.io/github/go-mod/go-version/shutx-net/micawber?logo=go&logoColor=white&color=00ADD8)](go.mod)

A portable, Git-native headless CMS, written in Go.

Micawber manages Markdown content without introducing a content database of its own. Git is
the source of truth: history, revisions, collaboration and rollback are Git's job, and
Micawber does not reimplement them.

- Markdown with front matter is the canonical content format.
- Binary assets are stored separately from content.
- Content and asset backends are replaceable; the core knows nothing about Git hosting
  providers or object storage vendors.
- It ships as a single Go binary. No SQL database, no managed service, no container
  required.

Portability is a product requirement rather than a preference. See [AGENTS.md](AGENTS.md)
for the architecture rules this repository is held to.

## Status

Early. The core is being built before anything user-facing, in the order set out in
AGENTS.md: domain models and interfaces first, then Markdown handling, Git-backed content
operations, asset storage, configuration, adapters, an HTTP API, and an admin UI last.

Done so far:

| | |
| --- | --- |
| `core/` | Domain models and the storage interfaces. Standard library only. |
| `markdown/` | Markdown and front-matter handling: YAML, TOML and JSON blocks, parsed and serialized byte-exactly. |
| `git/` | Content operations against a local Git repository, by driving the `git` binary with plumbing commands. No Git library. |
| `localfs/` | Asset storage in a local directory: atomic, durable writes and no metadata of its own. Standard library only. |

There is no released version and no stable API yet.

## Layout

```
core/      the domain model and the interfaces every adapter implements
markdown/  Markdown files to and from core.Content, byte for byte
git/       core.ContentRepository and core.ContentHistory over a local Git repository
localfs/   core.AssetStore over a local filesystem directory
```

`markdown` reads a document's front matter and writes it back unchanged unless a field
actually changed, so a Git diff shows the edit and nothing else.

`git` drives the user's `git` binary rather than linking a Git library, so a repository
Micawber writes stays an ordinary repository that ordinary `git` can read, and Micawber
never holds a credential. It needs `git` installed; nothing else does.

`localfs` keeps no index, sidecar or manifest: the directory is the whole store, so a file
copied into it by hand is an object on exactly the same terms as one Micawber wrote.

`core` imports only the standard library, and a test enforces it: provider code lives in
sibling packages that depend on `core`, never the other way round.

## License

MIT. See [LICENSE](LICENSE).
