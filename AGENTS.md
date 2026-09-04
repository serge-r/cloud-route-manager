# cloud-route-manager — working notes

A Go service that manages cloud route tables (AWS, Yandex Cloud). It scans the
configured route sources on an interval, points the collected prefixes at the
instance it runs on, and runs CLI commands after a successful or failed update.

This file is the specification and the house rules. `README.md` is the user
facing documentation; keep the two consistent.

## Specification

### Pass

At startup, and then every `general.interval`:

1. Detect the cloud (AWS / Yandex Cloud) from the instance metadata service,
   unless `general.cloud` pins it.
2. Detect the primary interface and its IPv4 address from the default route,
   unless `general.interface` / `general.ip-address` pin them.
3. Query **every** configured source in a single pass, merge the results,
   drop duplicates.
4. Update every route table in `destination.route-table-ids` so the collected
   prefixes point at the address from step 2, and reconcile
   `local-static-routes` into the routing table of the host itself.
5. Run the actions configured for the outcome. Nothing configured, nothing
   runs.

### Local static routes

`local-static-routes` is a top level list of `"<prefix> via <target>"`, where
the target is a gateway address, `default` (resolved from the current default
route on every pass) or `blackhole`. It is configuration only — the sources do
not feed it — and it is independent of the cloud half of a pass.

### Route sources

* common: static list in the config, file on the filesystem, object in S3
  (also S3-compatible endpoints);
* AWS specific: SSM parameter;
* Yandex Cloud specific: instance metadata attribute.

Source content may separate routes by commas, semicolons, spaces or newlines;
`#` and `//` start a comment. A prefix may be written with a mask or without
one — a bare address is a host route (`/32`, `/128` for IPv6). Prefixes are
canonicalised (host bits masked off) before use.

### Invariants

These are behavioural decisions, not accidents. Do not change them without the
maintainer asking for it.

* **Only the prefixes in the collected list are managed.** Missing ones are
  created; existing ones are overwritten to point here *regardless of their
  current next hop*; ones that already point here are left alone. Routes
  outside the collected list are never touched and never deleted.
* **A source failure does not discard the other sources.** The pass is marked
  failed, but the routes that were collected are still applied. If not a
  single route was collected, the route tables are not touched at all.
* **`success` actions run only when something actually changed**, and never
  under `dry-run`. `actions.run-on-start` additionally fires them once after
  the first pass. `failed` actions run on every failed pass.
* **Actions survive a cancelled pass**: they run on a context detached from
  the cycle timeout, so a timed out pass can still report the failure.
* **Local routes are marked with `proto 201`** (`localroutes.Proto`) and the
  service reads, replaces and deletes *only* routes carrying it. That tag is
  the ownership record: it survives a restart and a crash, so no state file is
  needed. Never widen the selection to untagged routes.
* **Local and cloud halves of a pass are independent.** A failure of one does
  not skip the other; local routes are applied even when no source produced a
  single route. Both feed the same "did anything change" decision.
* **Deleting is opt-in and local only**: `general.remove-stale-local-routes`
  removes `proto 201` routes that left the config. Cloud route tables are
  never cleaned up, whatever that flag says.
* **`dry-run` performs no mutating API call at all**, in either cloud, and
  runs no mutating `ip` command.
* **Every `general.*` config key has a command line flag of the same name.**
  A flag given explicitly wins; a flag left out keeps the file value.
  `main_test.go` enforces the parity — add both or neither.
* **Logging goes to stdout by default**, as text, at `info`.

### Publishing

Multi-arch Docker image (`linux/amd64`, `linux/arm64`), deb and rpm packages,
GitHub releases, `CHANGELOG.md`. Packages install under
`/opt/cloud-route-manager` (binary in `bin/`, config as `config.yml` marked
`config|noreplace`, systemd unit, `/usr/local/bin` symlink).

## Layout

```
main.go                    flags, config loading, wiring, signals
internal/config            YAML schema, defaults, validation, GeneralKeys()
internal/logging           slog handler from general.log-*
internal/netinfo           primary interface / address detection
internal/routes            parsing, normalisation, de-duplicated Set
internal/source            Source interface + static, file, s3, aws-ssm, yandex
internal/localroutes       spec parsing, host routing table through ip(8)
internal/cloud             Manager interface, Change type, cloud autodetection
internal/cloud/aws         EC2 route tables, ENI lookup (metadata, then API)
internal/cloud/yandex      VPC route tables, instance metadata, IAM token
internal/actions           placeholder expansion and command execution
internal/app               the scan-and-update loop, action dispatch
packaging/                 systemd unit, deb/rpm maintainer scripts
```

Adding a cloud means implementing `cloud.Manager` and extending
`cloud.Detect` plus `app.newManager`. Adding a source means implementing
`source.Source`, a config struct and a branch in `source.Build`.

## Conventions

* Go, standard library first. Dependencies: `gopkg.in/yaml.v3` and the AWS SDK
  v2. Yandex Cloud is plain `net/http` against the public REST API — do not
  pull in the Yandex SDK.
* Comments and identifiers in English. Comments explain *why*, not *what*.
* `log/slog` everywhere, structured key/value attributes, no `fmt.Println`.
* Errors are wrapped with context (`fmt.Errorf("...: %w", err)`); per-table and
  per-source failures are collected with `errors.Join` instead of aborting the
  pass on the first one.
* Cloud APIs are reached through small interfaces (`aws.EC2API`,
  `localroutes.Runner`, `app.localManager`) or plain HTTP so tests can fake
  them. No live cloud calls and no real `ip` invocations in tests.
* Unknown fields in the YAML are an error (`KnownFields(true)`) — a typo in a
  config key must not be silently ignored.
* Fields the service does not understand in a Yandex route table entry are
  preserved verbatim; the update sends back the whole `staticRoutes` list.

## Commands

```sh
make            # fmt + vet + test + build
make test       # go test ./...
make race       # go test -race ./...
make cover      # coverage summary
make tools      # install golangci-lint (see below)
make lint       # golangci-lint

make snapshot   # deb/rpm/tarballs into dist/ (goreleaser)
make docker     # local image
```

golangci-lint must be built from source with the toolchain from `go.mod`
(`make tools`, and `go install` in CI): the released binaries are compiled with
an older Go and refuse to analyse a module targeting a newer one. Do not
install it from a tarball or via the marketplace action.

Manual smoke test without a cloud: point `YC_METADATA_URL`, `YC_VPC_ENDPOINT`
and `YC_OPERATION_ENDPOINT` at a local stub, set `general.cloud: yandex` and
`general.ip-address`, then run with `-once -log-severity debug`.

## When changing things

* A new or renamed `general.*` key: update the struct tag, `Defaults()`, the
  flag in `newOptions`, `applyTo`, `config.example.yml` and the README table.
* Any user visible change: add a `CHANGELOG.md` entry under `Unreleased`.
* New behaviour: add a test next to the package it lives in; the existing
  tests fake the cloud APIs rather than skipping.
