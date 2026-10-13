# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]
### Added
- README verified-status banner per workflow#714 (multi-repo QoL sweep).
- CONTRIBUTING.md, examples/minimal/config.yaml, and GitHub issue/PR templates.

### Fixed
- Normalize native Windows separators for policy glob matching, including deny rules used by the executor.
- Validate Linux container mount destinations independently of host path conventions, preserving critical-directory denials.
- Correct platform-specific owner-suite fixtures and assertions while retaining the full Windows and Linux test suites.

## [v0.9.3] - 2026-05-19

Initial CHANGELOG entry tracking the current release. See git tags for prior versions.
