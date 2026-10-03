# usher

[![ci](https://github.com/mrcsin/usher/actions/workflows/ci.yml/badge.svg?branch=master&event=push)](https://github.com/mrcsin/usher/actions/workflows/ci.yml?query=branch%3Amaster+event%3Apush)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

<div align="center">
    <img src=./logo.png width=200 />
</div>

User management for the VPN services of one node. You list users and the interfaces each may use
in one file; within seconds usher creates their keys, puts them into the running services and
writes a ready client config per user and interface. Addresses and keys stay internal: you type
names only.

usher holds no capabilities and is never in the packet path. Every VPN service runs in its own
container with a small management API on a unix socket; usher calls that API and nothing else, so
a crash or an upgrade of usher leaves every session running. AmneziaWG is supported through
[awg-grpc](https://github.com/mrcsin/awg-grpc); Xray is next.

## Users

`config/usher.yml`:

```yaml
alice-phone: [awg0]
alice-laptop: [awg0, awg1]
bob: []
```

- One name per device: a WireGuard key cannot serve two devices at once.
- Removing a name, or setting it to `[]`, switches the user off. The keys stay, and putting the
  name back restores the same config.
- A broken file changes nothing: usher keeps the last valid one and logs the error with its line.
- A service that restarts empty is refilled within 30 s without any change to the file.

Client configs appear in `clients/<user>/<interface>.conf`. Secrets live in `state/users.json`,
the only copy of every key: keep the state volume in the backup set.

## Running

[`deploy/compose.example.yml`](deploy/compose.example.yml) runs usher next to awg-grpc. Images are
`ghcr.io/mrcsin/usher:<tag>`, one per release.

| Variable     | Example           | Meaning                                         |
| ------------ | ----------------- | ----------------------------------------------- |
| `USHER_HOST` | `203.0.113.10`    | IPv4 address clients connect to; no host names  |
| `USHER_DNS`  | `1.1.1.1,1.0.0.1` | DNS servers written into client configs         |

Both are required. Publish each interface port 1:1, and give usher the socket group of the
service containers.

To issue new keys for a user, stop usher, delete its `<interface>/<user>` entry from
`users.json` and start it again.

## License

[MIT](LICENSE)
