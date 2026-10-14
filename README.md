# usher

[![ci](https://github.com/mrcsin/usher/actions/workflows/ci.yml/badge.svg?branch=master&event=push)](https://github.com/mrcsin/usher/actions/workflows/ci.yml?query=branch%3Amaster+event%3Apush)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

<div align="center">
    <img src=./logo.png width=200 />
</div>

User management for the VPN services of one node. You list users and the interfaces each may use
in one file; within seconds usher creates their keys, puts them into the running services and
writes a ready client config per user and interface. Addresses and keys stay internal: you type
names only. An interface is an AmneziaWG interface or an inbound of your Xray config.

usher holds no capabilities and is never in the packet path. Every VPN service runs in its own
container with a small management API on a unix socket; usher calls that API and nothing else, so
a crash or an upgrade of usher leaves every session running. AmneziaWG is supported through
[awg-grpc](https://github.com/mrcsin/awg-grpc), Xray through its own gRPC API.

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
- A service that restarts empty is refilled at the next refill tick (every 30 s; Xray measured
  28 to 32 s, bounded at 45 s in the e2e) without any change to the file.

Client files appear in `clients/<user>/<interface>`: `.conf` for AmneziaWG, `.json` and `.txt`
for Xray. Secrets live in `state/users.json`, the only copy of every key: keep the state volume in
the backup set.

## Running

[`deploy/compose.example.yml`](deploy/compose.example.yml) runs usher next to awg-grpc and Xray.
Images are `ghcr.io/mrcsin/usher:<tag>`, one per release. Drop the service you do not use.

| Variable             | Example                  | Meaning                                              |
| -------------------- | ------------------------ | ---------------------------------------------------- |
| `USHER_HOST`         | `203.0.113.10`           | IPv4 address clients connect to; no host names       |
| `USHER_AWG_SOCKET`   | `/run/awg-grpc/awg.sock` | awg-grpc socket; enables the AmneziaWG backend       |
| `USHER_DNS`          | `1.1.1.1,1.0.0.1`        | DNS servers in AmneziaWG configs; needs the awg socket |
| `USHER_XRAY_SOCKET`  | `/run/xray/api.sock`     | Xray API socket; enables the Xray backend            |
| `USHER_XRAY_CONFIG`  | `/etc/xray/config.json`  | Xray server config, read-only; needs the Xray socket |

`USHER_HOST` is always required. Set at least one socket. Publish each interface port 1:1, and give
usher the socket group of the service containers.

To issue new keys for a user, stop usher, delete its `<interface>/<user>` entry from
`users.json` and start it again. The Xray entries are under `xray` and named `<tag>/<user>`. If you
change an inbound between a protocol with a UUID (VLESS, VMess) and one with a password (Trojan,
Hysteria2), the stored credentials no longer fit: usher reports "stored credential does not fit"
and keeps the old client files until you stop usher, delete the `<tag>/<user>` entries of that tag
and start it again.

### Upgrading

- `USHER_AWG_SOCKET` is required for the AmneziaWG backend; earlier releases hardcoded the path.
  Set it to the awg-grpc socket (`/run/awg-grpc/awg.sock` in the example) before you upgrade.
- The first save writes `users.json` as version 2 and copies the old file to `users.json.v1`. To
  roll back to an earlier image, stop usher and rename `users.json.v1` back to `users.json`.

## Xray

You write the Xray server config and usher fills its inbounds with users. An inbound tag is the
interface name in `usher.yml`, up to 32 characters. usher reads the config file and puts users
into the running inbounds through Xray's `HandlerService`. It never writes the config. Xray keeps
users in memory only, so usher refills them at the next refill tick after a restart.

Per user and inbound usher writes `clients/<user>/<tag>.json`, a full client config with a local
socks proxy on 127.0.0.1:10808 and an http proxy on 127.0.0.1:10809, and `clients/<user>/<tag>.txt`,
a share link.

### Server config

- One JSON file; comments are allowed. No `-confdir`.
- Mount the directory that holds the file into both containers, not the file. usher then sees a
  replaced file at its next refill tick.
- `api.services` is `["HandlerService"]` and `api.listen` is a unix path with `,0660`, such as
  `/run/xray/api.sock,0660`. The Xray process group must equal usher's group.
- The socket directory is a bind mount that usher can write. After a hard kill Xray leaves a stale
  socket and fails to start on it. usher unlinks the socket at its next tick, and Xray's restart
  policy (`restart: unless-stopped`) starts it again. usher unlinks a socket only when no process
  holds the `<socket>.lock` file that Xray keeps beside it, or, when that file is gone (a failed
  start removes it), when a connection to the socket is refused. A live Xray keeps its socket.
- Keep certificates and key files outside the directory that usher mounts. usher does not read
  them, and Xray reloads them itself. The example mounts `./xray-certs` into the Xray container
  only.
- The Xray image follows `xray-core` in `go.mod` (now `ghcr.io/xtls/xray-core:26.10.10`). Another
  version can change how Xray builds an inbound and put every inbound in drift.
- Every managed inbound has a tag, a single integer `port`, no unix `listen` and an empty
  `clients` list. A Hysteria2 inbound also has an empty `hysteriaSettings.auth`; usher gives each
  user an auth. VLESS `decryption` is `none`.

[`deploy/xray/config.json`](deploy/xray/config.json) is an example with placeholders that Xray
refuses to start with.

### Supported shapes

| Part      | Values                                                          |
| --------- | --------------------------------------------------------------- |
| Protocol  | VLESS, Hysteria2, VMess, Trojan                                 |
| Transport | raw (header none), ws, grpc, httpupgrade, xhttp, hysteria       |
| Security  | none, tls, reality                                              |
| Mask      | salamander (Hysteria2, without `packetSize`), `quicParams`      |

Shadowsocks and WireGuard inbounds are ignored. VMess and Trojan are covered by unit tests only.
An inbound outside this table fails; the log names the field. Its existing client files stay.

### The x-usher block

Add `x-usher` to an inbound to tell usher what clients need. Xray ignores the key. Every field is
optional, and an unknown key fails the inbound.

```json
"x-usher": {
  "address": "203.0.113.10",
  "port": 443,
  "sni": "example.com",
  "fingerprint": "chrome",
  "trust": "ca",
  "pin_sha256": "…",
  "hop_ports": "20000-30000"
}
```

| Field         | Default                              | Meaning                                              |
| ------------- | ------------------------------------ | ---------------------------------------------------- |
| `address`     | `USHER_HOST`                         | address clients dial                                 |
| `port`        | the inbound `port`                   | port clients dial                                    |
| `sni`         | `tlsSettings.serverName`, or the first Reality `serverNames` entry without `*` | name clients send; on Reality one of the `serverNames` entries |
| `fingerprint` | `chrome`                             | uTLS fingerprint; Reality allows `chrome`, `firefox`, `safari` |
| `trust`       | `ca`                                 | `ca` or `pin`, see below                             |
| `pin_sha256`  | hash of the inline certificate       | hex SHA-256 of the leaf; only with `trust: pin`      |
| `hop_ports`   | none                                 | Hysteria2 port range `from-to`, from < to            |

A TLS inbound with neither `sni` nor `tlsSettings.serverName` fails. Reality matches the name by
exact lookup in `serverNames`, so on Reality an `sni` that is not literally one of its entries
fails the inbound, and so does a config with only wildcard names. The privateKey and the
`mldsa65Seed` must be base64url keys of the right size, and the seed must differ from the key.

### TLS trust

- `ca`: the client verifies the server certificate against its CA roots and gets only the
  server name, the ALPN list and the fingerprint. Use a certificate that a public CA issued for
  your domain.
- `pin`: the client also gets `pinnedPeerCertSha256`, from `pin_sha256` or from an inline
  `certificates[].certificate`. An inbound with neither fails. A Hysteria2 pin inbound gets no
  `.txt`, because v2rayNG and Hiddify do not read the pin from a `hysteria2://` link.

Reality needs no certificate. usher derives the public key from `privateKey`, takes `shortIds[0]`
and sets `spiderX` to `/`. With `mldsa65Seed` it adds `mldsa65Verify`. Private keys, seeds, key
files and fallbacks never reach `clients/` or the log.

### Client compatibility

Measured on 2026-10-14 against Xray 26.10.10.

- Reality links work in Xray-core clients only. sing-box 1.14.3 fails the server's
  `X25519MLKEM768` requirement. Only `chrome`, `firefox` and `safari` carry it.
- xhttp is not in upstream sing-box.
- VLESS ws, Trojan, VMess and Hysteria2 with salamander work in sing-box.
- A user in `usher.yml` that loses an interface loses new connections at once. An open Hysteria2
  QUIC connection stays up until the client reconnects.
- `hop_ports` needs a DNAT on the host from the range to the inbound port. usher does not set it.

### Config edits

usher compares each inbound with the running Xray at every pass. After you edit a managed inbound
in the file, restart Xray. Until then usher logs a drift reason that names the part (port, proxy
settings, stream settings), keeps the old client files and still removes users who left
`usher.yml`. Certificate files that the config names are reloaded by Xray itself and cause no
drift. When the running inbound is missing or runs another protocol, usher changes nothing on it,
because an account of the wrong type crashes Xray; restart Xray.

An inbound that fails validation (an `x-usher` typo, a bad Reality key) also keeps its old client
files, and usher still removes the users who left `usher.yml` from it. It adds no user and
changes no credential until you fix the file.

## License

[MIT](LICENSE)
