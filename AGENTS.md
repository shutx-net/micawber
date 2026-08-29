# AGENTS.md

## Project

Micawber is a portable, Git-native headless CMS written in Go.

It manages Markdown content without introducing its own content database.

Core goals:

* Git is the source of truth for content and revision history.
* Markdown with front matter is the canonical content format.
* Binary assets are stored separately from content.
* Content and asset backends are replaceable.
* The core must not depend on a specific cloud or Git provider.
* Micawber should be distributable as a standalone Go binary.
* No external SQL database should be required.

Portability is a product requirement.

## Current priority

Build the core before the UI.

Prioritize:

1. Domain models
2. Core interfaces
3. Markdown/front matter handling
4. Git-backed content operations
5. Asset storage abstraction
6. Configuration
7. Tests
8. Infrastructure adapters
9. HTTP API
10. Admin UI

Do not build a complete CMS web application unless explicitly requested.

## Architecture

Dependencies must point toward the core.

The core defines domain concepts and interfaces such as:

```go
type ContentRepository interface {
    List(ctx context.Context, collection string) ([]Content, error)
    Get(ctx context.Context, path string) (*Content, error)
    Put(ctx context.Context, content Content) error
    Delete(ctx context.Context, path string) error
}
```

```go
type AssetStore interface {
    Put(ctx context.Context, asset Asset) (*AssetRef, error)
    Delete(ctx context.Context, key string) error
}
```

These are examples of intent, not fixed APIs.

Provider implementations belong outside the core.

Examples:

* Git
* GitHub
* GitLab
* S3-compatible storage
* Local filesystem

The core must not import AWS, GitHub, GitLab, or similar provider SDKs.

## Content

Prefer Markdown plus structured front matter.

Do not introduce a proprietary content format without a strong reason.

Preserve user-authored Markdown as faithfully as practical.

Avoid unnecessary rewrites that create noisy Git diffs.

Git provides persistence, history, revisions, collaboration, and rollback.

Do not recreate those capabilities in another database.

## Assets

Binary assets are separate from Git content.

Design around a generic asset storage abstraction.

Initial remote storage may use S3-compatible APIs, but AWS-specific behavior must not leak into the domain model.

CDNs such as CloudFront are delivery concerns, not storage requirements.

## Portability

Do not assume:

* AWS
* GitHub
* Docker
* Kubernetes
* a managed database
* a specific CI/CD service

Prefer standards and replaceable boundaries:

* Git
* HTTP
* filesystem
* S3-compatible object storage
* configuration

Docker support is useful but must not be required.

## Go

Prefer idiomatic, straightforward Go.

* Use the standard library when practical.
* Keep dependencies small and focused.
* Pass `context.Context` through I/O operations.
* Wrap errors with useful context.
* Keep interfaces small.
* Avoid global mutable state.
* Avoid unnecessary abstraction.
* Run `gofmt`.

Do not add a framework merely to avoid writing a small amount of ordinary Go.

## Testing

New behavior should normally include tests.

Prioritize tests around:

* Markdown/front matter
* path handling
* Git operations
* error and conflict behavior
* adapter contracts

Core tests must not require external cloud services or developer credentials.

## Security

Never commit or log credentials.

Treat Git credentials, SSH keys, tokens, and object-storage credentials as secrets.

Validate paths and asset keys against traversal or escaping configured roots.

Provider authentication belongs outside the core.

## Scope

Micawber is not:

* a static site generator
* a website framework
* a Git hosting service
* an object storage service
* a CDN
* a relational content database
* a deployment platform

Integrate with existing systems instead of recreating them.

## Agent workflow

Before changing code:

1. Read relevant code and tests.
2. Preserve architectural boundaries.
3. Make the smallest clean change.
4. Add or update tests.
5. Update documentation when architecture changes.

Do not introduce databases, frameworks, or provider-specific core dependencies without a clear requirement.

Detailed architecture belongs under `docs/`, not in this file.
