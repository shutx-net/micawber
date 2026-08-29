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

There is no released version and no stable API yet.

## Layout

```
core/     the domain model and the interfaces every adapter implements
```

`core` imports only the standard library, and a test enforces it: provider code will live in
sibling packages that depend on `core`, never the other way round.

## License

MIT. See [LICENSE](LICENSE).
