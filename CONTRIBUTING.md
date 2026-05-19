# Contributing to workflow-plugin-agent

This plugin is part of the [GoCodeAlone/workflow](https://github.com/GoCodeAlone/workflow) ecosystem.

## Before contributing

Read the [upstream CONTRIBUTING.md](https://github.com/GoCodeAlone/workflow/blob/main/CONTRIBUTING.md) for general conventions, signing, and review expectations.

## Local development

```sh
git clone https://github.com/GoCodeAlone/workflow-plugin-agent.git
cd workflow-plugin-agent
go build ./...
go test ./...
```

## Pull requests

- One feature or bugfix per PR.
- Update CHANGELOG.md with a Keep-a-Changelog entry.
- Add tests covering new behavior.
- Run `go vet ./...` before pushing.

## Reporting issues

See the issue templates under `.github/ISSUE_TEMPLATE/`.
