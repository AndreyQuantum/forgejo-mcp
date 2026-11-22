# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- MCP server for Forgejo (stdio and streamable HTTP) with 30 consolidated tools selected by an `action` enum.
- Compact plain-text output, server-side CI waiting and log processing (`ci_wait`, `ci_failure`).
- Toolsets, read-only mode and a deny list.
- `forgejo_api` and `forgejo_endpoints` for every API operation without a dedicated tool.
- Release engineering: CI workflows, GoReleaser configuration, Docker image, MCP registry manifest.
