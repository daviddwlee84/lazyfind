# Release distribution

Status: completed 2026-09-25 in [v0.1.2](https://github.com/daviddwlee84/lazyfind/releases/tag/v0.1.2).

macOS/Linux amd64/arm64 archives, checksums, a filtered source archive and shell
completions are published. The personal tap centrally validates and packages the
binaries. `upgrade --check` is read-only; explicit upgrade delegates a verified
Homebrew owner. Standalone copies keep the external installation path, including
chezmoi's `just upgrade-personal`; no download failure falls back to compilation.

Native Go/PTY CI, independent source/module archive builds, public fixed-tag and
`@latest` Go installs, and owner/changed-target/failure fixtures were verified.
The application is MIT licensed. The search/history/action contracts were not
changed by packaging. See [current distribution instructions](../docs/distribution.md).
