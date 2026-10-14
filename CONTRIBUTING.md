# Contributing

Issues and pull requests are welcome. Please follow the [code of conduct](CODE_OF_CONDUCT.md).

Before opening a pull request, run `make check-all`. It builds, runs the unit tests and the live tests, and runs every linter. The live tests need Docker; see [docs/development.md](docs/development.md).

## Releases

- Changes collect under "Unreleased" at the top of [CHANGELOG.md](CHANGELOG.md).
- A version is tagged when the batch is worth upgrading for, not on a schedule.
- Breaking changes are grouped into one release, not shipped one per release.
- Past changelog entries are not rewritten.
