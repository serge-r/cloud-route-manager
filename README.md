# cloud-route-manager

Keeps AWS and Yandex Cloud route tables pointing at the instance it runs on.
It periodically scans the configured route sources, points every collected
prefix at this machine, and runs the commands you configured for a successful
or a failed update.

Typical use: a VPN / overlay gateway (ZeroTier, WireGuard, IPsec) announces the
prefixes it serves through S3, SSM or a file, and every gateway instance keeps
the VPC route tables in sync with that list without anyone touching the console.

## How it works

At startup, and then on every pass (`general.interval`):

1. **Detect the cloud** — AWS or Yandex Cloud, from the instance metadata
   service. Set `general.cloud` to skip the probe.
2. **Detect the primary interface and its address** from the default route.
   Override with `general.interface` / `general.ip-address`.
3. **Query every configured source in one pass**, merge the results and drop
   duplicates.
4. **Update the route tables** listed in `destination.route-table-ids`, so that
   each collected prefix points at the address found in step 2, and install the
   entries of `local-static-routes` into the routing table of the host itself.
5. **Run the actions** from `actions`: `success` when routes were actually
   changed, `failed` when the pass failed. No actions configured, nothing runs.

### What exactly is changed

Only the prefixes in the collected list are managed:

| Situation | Result |
|---|---|
| the prefix is missing from the table | the route is created (`create`) |
| the prefix exists with a different next hop (another ENI, NAT, gateway, …) | the route is overwritten to point here (`replace`) |
| the prefix exists and already points here | nothing (`noop`) |
| a route that is not in the source list | left untouched |

Nothing is ever deleted, so `local`, `igw`, `nat` and any other routes you keep
in the same table survive.

## Install

### deb / rpm

Packages from the [releases](https://github.com/serge-r/cloud-route-manager/releases)
install under `/opt/cloud-route-manager`:

```
/opt/cloud-route-manager/bin/cloud-route-manager   # binary
/opt/cloud-route-manager/config.yml                # config (config|noreplace)
/opt/cloud-route-manager/config.example.yml        # annotated example
/usr/local/bin/cloud-route-manager                 # symlink
/lib/systemd/system/cloud-route-manager.service    # unit (rpm: /usr/lib/...)
```

```sh
sudo dpkg -i cloud-route-manager_*_amd64.deb        # or: sudo rpm -i ...
sudo vi /opt/cloud-route-manager/config.yml
cloud-route-manager -config /opt/cloud-route-manager/config.yml -check-config
sudo systemctl enable --now cloud-route-manager
```

`systemctl reload cloud-route-manager` (SIGHUP) forces an immediate pass
without restarting the service.

### Docker

Multi-arch image for `linux/amd64` and `linux/arm64`:

```sh
docker run --rm --network host \
  -v /path/to/config.yml:/opt/cloud-route-manager/config.yml:ro \
  ghcr.io/serge-r/cloud-route-manager:latest
```

Add `--cap-add NET_ADMIN` when using `local-static-routes`.

`--network host` lets the service see the host's default route and reach the
instance metadata service. Without it, pin `general.ip-address`.

### From source

```sh
make build          # -> dist/cloud-route-manager
```

## Configuration

The annotated reference is [`config.example.yml`](config.example.yml).

Every key of the `general` section has a command line flag with the same name.
A flag that is given explicitly wins over the file; a flag that is not given
leaves the file value alone.

| `general` key | Flag | Default | Meaning |
|---|---|---|---|
| `interval` | `-interval` | `10m` | time between passes; `0` means one pass and exit |
| `log-severity` | `-log-severity` | `info` | `debug`, `info`, `warn` or `error` |
| `log-file` | `-log-file` | `stdout` | `stdout`, `stderr` or a path |
| `log-format` | `-log-format` | `text` | `text` or `json` |
| `dry-run` | `-dry-run` | `false` | compute and log the changes, call no cloud API |
| `cloud` | `-cloud` | `auto` | `auto`, `aws` or `yandex` |
| `interface` | `-interface` | detected | pin the primary interface |
| `ip-address` | `-ip-address` | detected | pin the next hop address |
| `timeout` | `-timeout` | `2m` | budget for one pass |
| `action-timeout` | `-action-timeout` | `1m` | budget for one action command |
| `remove-stale-local-routes` | `-remove-stale-local-routes` | `false` | delete local routes of this service that left the config |

Flags without a configuration key:

```
-config string    path to the configuration file (default /opt/cloud-route-manager/config.yml)
-once             run a single pass and exit (same as -interval 0)
-check-config     validate the configuration, including the flag overrides, and exit
-version          print the build version
```

### Sources

Every configured source is queried on each pass and the results are merged.
Routes may be separated by commas, semicolons, spaces or newlines; text after
`#` or `//` is ignored. A prefix may be written with or without a mask — a bare
address is a host route (`/32`, or `/128` for IPv6).

```yaml
source:
  static:                       # a list kept in this file
    routes: [1.1.1.1, 10.0.0.0/8]
  file:                         # a file on the local filesystem
    path: /opt/cloud-route-manager/routes.txt
    optional: true              # a missing file is an empty list, not an error
  s3:                           # an object in S3 or an S3-compatible storage
    region: eu-north-1
    bucket: configs
    path: data/routes.cfg
    endpoint: https://storage.yandexcloud.net   # optional
    use-path-style: true                        # optional
  aws-ssm:                      # an SSM parameter (String, StringList, SecureString)
    path: /zerotier/eu-north-1/routes
  yandex-instance-metadata:     # an instance metadata attribute
    key: routes
```

If a source fails, the pass is marked failed but the routes from the remaining
sources are still applied. If no source yields a single route, the route tables
are left alone and the `failed` actions run.

### Destination

```yaml
destination:
  route-table-ids:
    - rtb-0123456789abcdef0     # AWS
    - enp1abcd2efgh3ijkl4mn     # Yandex Cloud
```

### Local static routes

Besides the cloud route tables, the service can maintain static routes in the
routing table of the host it runs on. The list is independent of `source`: it
lives in the configuration file and is reconciled on every pass, so a route
someone deleted by hand comes back.

```yaml
local-static-routes:
  - "192.168.0.0/24 via default"      # via the current default gateway
  - "10.10.0.0/16 via 1.1.1.1"        # via an explicit gateway
  - "172.16.5.0/24 via blackhole"     # drop the traffic
```

`via default` is re-resolved on every pass, so the route follows a default
gateway that changed. The same create / overwrite / leave-alone rules as for
the cloud apply, with one difference: `general.remove-stale-local-routes: true`
additionally deletes routes that this service installed earlier and that are no
longer in the list.

Routes are installed through `ip route replace` and tagged with **`proto 201`**.
That tag is how the service tells its own routes from everyone else's — it only
ever looks at, replaces and deletes routes carrying it, so DHCP, kernel and
routing-daemon entries are never affected. To see what is being managed:

```sh
ip route show proto 201
```

This needs `ip(8)` (package `iproute2`, already in the Docker image) and
`CAP_NET_ADMIN` — that is, root, or `docker run --cap-add NET_ADMIN`. If you do
not configure `local-static-routes`, none of this is used and no privileges
beyond reading metadata are required.

### Actions

```yaml
actions:
  run-on-start: false           # also run the success commands after the first
                                # pass, even when nothing had to change
  success:
    - "/usr/local/bin/reload.sh ${ip-address}"
  failed:
    - "/usr/local/bin/failed.sh ${routes}"
```

Commands run one after another through `/bin/sh -c`. A failing command is
logged and does not stop the rest.

| Placeholder | Environment variable | Value |
|---|---|---|
| `${routes}` | `CRM_ROUTES` | comma separated route list |
| `${ip-address}` | `CRM_IP_ADDRESS` | address of the primary interface |
| `${interface}` | `CRM_INTERFACE` | name of the primary interface |
| `${cloud}` | `CRM_CLOUD` | `aws` or `yandex` |
| `${routes-count}` | — | number of routes |

`success` runs only when changes were really applied — never under `dry-run`.
`failed` runs on every failed pass.

## Permissions

**AWS** — instance role:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow",
      "Action": ["ec2:DescribeRouteTables", "ec2:DescribeNetworkInterfaces",
                 "ec2:CreateRoute", "ec2:ReplaceRoute"],
      "Resource": "*" },
    { "Effect": "Allow", "Action": "ssm:GetParameter",
      "Resource": "arn:aws:ssm:*:*:parameter/zerotier/*" },
    { "Effect": "Allow", "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::configs/*" }
  ]
}
```

Credentials come from the standard AWS chain (instance role, environment,
shared config).

**Yandex Cloud** — attach a service account with the `vpc.admin` (or
`vpc.privateAdmin`) role on the folder that holds the route tables. The IAM
token is taken from the instance metadata service.

## Troubleshooting

```sh
# see what would happen, without touching anything
cloud-route-manager -config config.yml -once -dry-run -log-severity debug
```

Environment variables that redirect the service at a different endpoint, useful
when testing outside a cloud:

| Variable | Purpose |
|---|---|
| `CRM_METADATA_URL` | metadata service used for cloud autodetection |
| `YC_METADATA_URL` | Yandex Cloud instance metadata service |
| `YC_VPC_ENDPOINT` | Yandex Cloud VPC API |
| `YC_OPERATION_ENDPOINT` | Yandex Cloud operation API |
| `YC_IAM_TOKEN` / `YC_TOKEN` | IAM token, instead of asking the metadata service |

## Development

```sh
make            # fmt + vet + test + build
make cover      # test coverage summary
make race       # tests under the race detector
make snapshot   # deb, rpm and tarballs into dist/ via goreleaser
make docker     # local image
```

Layout:

```
main.go                    flags, wiring
internal/config            YAML schema, defaults, validation
internal/logging           slog setup
internal/netinfo           primary interface detection
internal/routes            route parsing and de-duplication
internal/source            static, file, s3, aws-ssm, yandex metadata sources
internal/localroutes       static routes in the host routing table via ip(8)
internal/cloud             provider interface and cloud autodetection
internal/cloud/aws         EC2 route tables
internal/cloud/yandex      VPC route tables and instance metadata
internal/actions           command execution and placeholder substitution
internal/app               the scan-and-update loop
packaging/                 systemd unit and package scripts
```

A `vX.Y.Z` tag builds the binaries, the deb/rpm packages and the multi-arch
image, and publishes a GitHub release. Changes are recorded in
[CHANGELOG.md](CHANGELOG.md).

## License

[MIT](LICENSE)
