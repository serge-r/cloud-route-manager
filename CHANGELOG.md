# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `local-static-routes`: static routes maintained in the routing table of the
  host itself, written as `"<prefix> via <gateway|default|blackhole>"`.
  `via default` follows the current default gateway and is re-resolved on
  every pass. Routes are installed with `ip route replace` and tagged with
  `proto 201`, so the service only ever touches routes it owns.
- `general.remove-stale-local-routes` (and the matching
  `-remove-stale-local-routes` flag) deletes local routes of this service that
  are no longer listed in the configuration. Off by default.
- `iproute2` in the Docker image; `local-static-routes` needs `CAP_NET_ADMIN`.

## [0.1.0] - 2026-09-04

First release.

### Added

- Periodic scanning of route sources and updating of AWS and Yandex Cloud
  route tables.
- Cloud autodetection (AWS / Yandex Cloud) from the instance metadata service,
  and primary interface/address detection from the default route.
- Route sources: static list in the configuration, file on the filesystem,
  object in S3 (including S3-compatible endpoints), AWS SSM parameter and
  Yandex Cloud instance metadata attribute. All sources are queried in a
  single pass and the results are merged without duplicates.
- Parsing of routes separated by commas or newlines, with or without a mask
  (a bare address becomes a `/32`, IPv6 a `/128`).
- Only the collected prefixes are managed: missing ones are created, existing
  ones are overwritten regardless of their current next hop, and every other
  route is left untouched.
- `dry-run` mode and a `-check-config` flag.
- `success` / `failed` actions with `${routes}`, `${ip-address}`,
  `${interface}`, `${cloud}` and `${routes-count}` substitutions, mirrored by
  `CRM_*` environment variables.
- A command line flag for every `general.*` configuration key; an explicit
  flag overrides the file.
- Logging to stdout, stderr or a file, in text or JSON format.
- Out-of-schedule pass on SIGHUP (`systemctl reload`).
- Publishing: multi-arch Docker image (`linux/amd64`, `linux/arm64`), deb and
  rpm packages installing into `/opt/cloud-route-manager`, a systemd unit and
  GitHub releases.

[Unreleased]: https://github.com/serge-r/cloud-route-manager/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/serge-r/cloud-route-manager/releases/tag/v0.1.0
