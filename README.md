# usher

[![ci](https://github.com/mrcsin/usher/actions/workflows/ci.yml/badge.svg?branch=master&event=push)](https://github.com/mrcsin/usher/actions/workflows/ci.yml?query=branch%3Amaster+event%3Apush)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

<div align="center">
    <img src=./logo.png width=200 />
</div>

User management for the VPN services of one node. You list users and the interfaces each may use
in one file; within seconds usher creates their credentials, puts them into the running services
and writes a ready client config or link per user and interface. Addresses, keys and UUIDs stay
internal: you type names only.

usher holds no capabilities and is never in the packet path. Every VPN service runs in its own
container with a small management API on a unix socket; usher calls that API and nothing else, so
a crash or an upgrade of usher leaves every session running. usher supports two services:
AmneziaWG through [awg-grpc](https://github.com/mrcsin/awg-grpc), and Xray through the API of Xray
itself.

## Users

`config/usher.yml`:

```yaml
alice-phone: [awg0]
alice-laptop: [awg0, awg1]
bob: []
```

- An interface name is an AmneziaWG interface or the tag of an Xray inbound. A name that both
  services report fails in both, so keep the names apart.
- One name per device on AmneziaWG: a WireGuard key cannot serve two devices at once.
- Removing a name, or setting it to `[]`, switches the user off. The credentials stay, and putting
  the name back restores the same config or link.
- A broken file changes nothing: usher keeps the last valid one and logs the error with its line.
- A service that restarts empty is refilled within 30 s without any change to the file.

Client files appear in `clients/<user>/<interface>.conf` for AmneziaWG and in
`clients/<user>/<tag>.txt` for Xray. An Xray file holds one `vless://` link. Secrets live in
`state/users.json`, the only copy of every key and UUID: keep the state volume in the backup set.
`users.json` has format version 2. Once this version has saved it, the previous usher image
cannot read it, so a rollback to that image needs a copy of `users.json` from before the upgrade.

### Switching off

On AmneziaWG, removing a user removes the peer from the interface. On Xray, removing a user
removes it from the inbound: new connections fail, but connections that are already open stay
until the client closes them or Xray ends them after its `connIdle` timeout.

## Running

[`deploy/compose.example.yml`](deploy/compose.example.yml) runs usher next to awg-grpc and Xray.
Images are `ghcr.io/mrcsin/usher:<tag>`, one per release.

| Variable            | Example                  | Meaning                                        |
| ------------------- | ------------------------ | ---------------------------------------------- |
| `USHER_HOST`        | `203.0.113.10`           | IPv4 address clients connect to; no host names |
| `USHER_DNS`         | `1.1.1.1,1.0.0.1`        | DNS servers written into AmneziaWG configs     |
| `USHER_AWG_SOCKET`  | `/run/awg-grpc/awg.sock` | absolute path of the awg-grpc socket           |
| `USHER_XRAY_SOCKET` | `/run/xray/api.sock`     | absolute path of the Xray API socket           |

`USHER_HOST` is required. Set `USHER_AWG_SOCKET`, `USHER_XRAY_SOCKET` or both; a variable that is
set turns its service on, and usher exits at start when neither is set. `USHER_DNS` is required
with `USHER_AWG_SOCKET` and is not read without it. A deployment from before the Xray driver must
add `USHER_AWG_SOCKET` to its compose file.

Publish each interface port 1:1, and give usher the socket group of the service containers.

If one service is down during a pass, usher logs it, applies the other service and keeps the
client files of the service that is down.

### Xray

usher creates no inbound and starts no Xray process. It reads the VLESS inbounds of a running
Xray and manages their users. Prepare the Xray container as follows:

- Use Xray v24.11.5 or later, which has `GetInboundUsers`.
- Set `api.listen` to a unix socket path with the mode suffix, for example
  `/run/xray/api.sock,0660`. Xray gives the socket its own uid and gid.
- Run Xray with the group of usher (`user: "0:1000"` in the example), so usher can open the
  socket.
- List only `HandlerService` in `api.services`.
- Start every VLESS inbound with `"clients": []`. usher adds the users.
- Name each inbound tag so that it fits the interface name pattern: up to 15 characters from
  letters, digits and `_=+.-`, and the first one a letter, a digit or `_`.
- After a hard kill, remove the stale socket file before you start Xray again. The official image
  has no shell, so remove it through the socket volume.

usher supports one inbound shape: VLESS with `decryption: none` over raw TCP without a header,
with Reality security. usher gives every user the flow `xtls-rprx-vision`. An inbound with another
security, transport, header or decryption, or with a port range, fails with an error that names
the field; the other inbounds keep working. An inbound with another protocol, such as the `api`
tunnel, is left alone. An unreferenced VLESS inbound gets an empty user list.

[`deploy/xray/config.json`](deploy/xray/config.json) is an example inbound. Replace its Reality
private key and short ID before use; `xray x25519` prints a key pair.

The link holds the public key derived from the inbound's private key, the first non-empty server
name, the first short ID with trailing zero bytes removed, and the fingerprint `chrome`. usher
sends no ML-DSA-65 verification key, so the link has no `pqv` parameter.

Xray 26.x blocks private destinations for VLESS by default. If your clients must reach a private
address through the node, allow it in the `finalRules` of the freedom outbound, as the e2e server
config in `deploy/e2e/xray/server.json` does for its target.

To issue new keys for a user, stop usher, delete its `<interface>/<user>` entry from
`users.json` and start it again.

## License

[MIT](LICENSE)
