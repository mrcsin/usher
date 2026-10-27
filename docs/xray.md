# Xray

This page covers the server config contract, the supported shapes, the `x-usher` block, TLS
trust, client compatibility, stats and config edits.

## Server config

- Xray starts with one config file (`-config`), and usher reads the same file. Xray's `-confdir`
  mode, which merges several files, is not supported. The file may contain `//`, `/* */` and `#`
  comments.
- Mount the directory that holds the file into both containers, not the file itself. An editor
  replaces the file, and a bind mount of a single file keeps showing the old one.
- `api.services` is `["HandlerService"]`, plus `StatsService` for [stats](#stats), and
  `api.listen` is a unix socket with mode `0660`, such as `/run/xray/api.sock,0660`.
- The socket directory is a bind mount that usher can write. A hard kill leaves a stale socket,
  and Xray then fails to start. usher removes that socket, and the restart policy
  (`restart: unless-stopped`) starts Xray again. usher never removes the socket of a running Xray.
- Keep certificates and key files in a directory that only the Xray container mounts
  (`./xray-certs` in the example): usher needs no access to private keys. Xray reloads changed
  certificate files itself.
- Use the Xray image of the example compose file. usher compares every inbound with the running
  Xray, and another Xray version can build an inbound differently, so every inbound reports a
  drift (see [Config edits](#config-edits)).
- Every managed inbound has a tag, one integer `port`, no unix `listen` and an empty `clients`
  list. A tag has up to 32 letters, digits, `_`, `=`, `+`, `.` and `-`, not starting with `=`,
  `+`, `.` or `-`. VLESS has `decryption: none`. Hysteria2 has `security: tls`, `version: 2` in both
  `settings` and `hysteriaSettings`, and an empty `hysteriaSettings.auth`; usher gives each user an
  auth.

[`deploy/xray/config.json`](../deploy/xray/config.json) is an example. Replace its
`REPLACE_WITH_*` placeholders; Xray does not start with them.

## Supported shapes

| Part      | Values                                                     |
| --------- | ---------------------------------------------------------- |
| Protocol  | VLESS, Hysteria2, VMess, Trojan                            |
| Transport | raw (header none), ws, grpc, httpupgrade, xhttp, hysteria  |
| Security  | none, tls, reality; Hysteria2 needs tls                    |
| Mask      | salamander, on Hysteria2 only and without `packetSize`     |

usher manages only inbounds with a tag and one of these protocols; it skips all others. An
inbound outside this table fails; the log names the field, and its existing client files stay.
Other `finalmask` keys, such as `quicParams`, stay on the server: usher does not copy them to
clients. The end-to-end tests cover VLESS (Reality, xhttp, ws) and Hysteria2; the other
combinations have unit tests only.

## The x-usher block

Add `x-usher` to an inbound to tell usher what clients need. Xray ignores the key. Every field is
optional, and an unknown key fails the inbound. An example for a Hysteria2 inbound:

```json
"x-usher": {
  "address": "203.0.113.10",
  "port": 443,
  "sni": "vpn.example.com",
  "fingerprint": "chrome",
  "trust": "ca",
  "hop_ports": "20000-30000"
}
```

| Field         | Default                              | Meaning                                              |
| ------------- | ------------------------------------ | ---------------------------------------------------- |
| `address`     | `USHER_HOST`                         | address clients dial                                 |
| `port`        | the inbound `port`                   | port clients dial                                    |
| `sni`         | `tlsSettings.serverName`, or the first Reality `serverNames` entry without `*` | name clients send; on Reality one of the `serverNames` entries |
| `fingerprint` | `chrome`                             | uTLS fingerprint; Reality allows `chrome`, `firefox`, `safari` |
| `trust`       | `ca`                                 | `ca` or `pin`, see [TLS trust](#tls-trust)           |
| `pin_sha256`  | hash of the inline certificate       | 64 hex characters, SHA-256 of the leaf; only with `trust: pin` |
| `hop_ports`   | none                                 | Hysteria2 only: port range `from-to`, from < to      |

A TLS inbound with neither `sni` nor `tlsSettings.serverName` fails. Reality matches the name by
exact lookup in `serverNames`, so on Reality an `sni` that is not literally one of its entries
fails the inbound, and so does a config with only wildcard names. The `privateKey` and the
`mldsa65Seed` must be base64url keys of the right size, and the seed must differ from the key.

`hop_ports` needs a DNAT on the host from the range to the inbound port. usher does not set it.

## TLS trust

- `ca`: the client verifies the server certificate against its CA roots and gets only the
  server name, the ALPN list and the fingerprint. Use a certificate that a public CA issued for
  your domain.
- `pin`: needs `security: tls`. The client also gets `pinnedPeerCertSha256`, from `pin_sha256` or
  from an inline `certificates[].certificate`. An inbound with neither fails. A Hysteria2 pin
  inbound gets no `.txt`, because v2rayNG and Hiddify do not read the pin from a `hysteria2://`
  link.

Reality needs no certificate. usher derives the public key from `privateKey`, takes `shortIds[0]`
and sets `spiderX` to `/`. With `mldsa65Seed` it adds `mldsa65Verify`. Private keys, seeds, key
files and fallbacks never reach `clients/` or the log.

## Client compatibility

Checked on 2026-10-14 with Xray-core 26.10.10 and sing-box 1.14.3. A new Xray, uTLS or sing-box
release can change this.

- Xray-core connects to every supported shape. Since Xray 26.9.8 the Reality server accepts a
  ClientHello only with an `X25519MLKEM768` key share. The uTLS fingerprints `chrome`, `firefox`
  and `safari` send it, and usher fails a Reality inbound with any other `x-usher.fingerprint`. A
  client core before 26.3.27 sends `firefox` without the share and fails; `chrome` connects there.
- sing-box fails Reality on every fingerprint and has no xhttp transport. VLESS ws, Trojan, VMess
  and Hysteria2 with salamander work.

## Stats

With `USHER_STATUS_DIR` set, usher reads per-user traffic and online state through Xray's
`StatsService` for the status file of the [README](../README.md#stats). Xray counts them only with
these switches, all present in [`deploy/xray/config.json`](../deploy/xray/config.json):

```json
"api": { "services": ["HandlerService", "StatsService"] },
"stats": {},
"policy": {
  "levels": {
    "0": { "statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true }
  }
}
```

usher creates users at level 0. Xray merges a level onto its default policy, so these keys leave
the timeouts unchanged. Xray reads them only at start: restart Xray after you add them.

usher checks the file for the switches at every pass. When one is missing or has the wrong type, it
logs `reading stats` with `stats are off in the xray config:` and the missing keys, for every
inbound and every pass, and writes no user series for Xray. `usher_interface_up` is not affected.
usher matches the service name without regard to case, as Xray does.

usher reads the file, not the running Xray. After you add the switches and before Xray restarts,
two cases differ. When the running Xray has no `StatsService`, usher logs `reading stats`.
When it has `StatsService` but not the other switches, Xray counts nothing, and usher writes 0
bytes and online 0 for every user and logs no error.

- Online means the user has at least one open proxied connection. A client that is idle with no
  connection reads 0. A quiet connection keeps it at 1 until Xray's `connIdle` closes it, 300 s by
  default.
- Xray does not count connections from `127.0.0.1` or `[::1]`, so a client that reaches Xray
  through a local proxy in front of it never reads online.
- Xray creates the traffic counters of a user at the first connection. Until then the user reads
  0 bytes.
- Xray keeps the counters of a removed user until it restarts. A user who is removed and added
  back continues from the old value.

## Removing a user

A user who loses an interface in `usher.yml` cannot open new connections on it. An open Hysteria2
QUIC connection stays up until the client reconnects.

## Config edits

After you edit a managed inbound in the file, restart Xray. Until then the inbound in the file
differs from the running one, and usher logs a drift that names the differing part: proxy type,
proxy settings, port, stream settings or receiver settings. Certificate files that the config
names are reloaded by Xray itself and cause no drift.

If you change an inbound between a protocol with a UUID (VLESS, VMess) and one with a password
(Trojan, Hysteria2), the stored credentials no longer fit. usher logs "stored credential does not
fit" and keeps the old client files until you delete the `<tag>/<user>` entries of that tag as in
[New keys for a user](operations.md#new-keys-for-a-user).

When an inbound has a problem, usher keeps its old client files and treats its users by the first
row that matches:

| Problem                                                                       | Users                                                    |
| ----------------------------------------------------------------------------- | -------------------------------------------------------- |
| The inbound fails a check of the file (port, `listen`, `clients`, transport, security, masks, Hysteria2 fields, Reality keys, a tag that is invalid or defined twice) | unchanged                                                |
| The running inbound is missing, runs another protocol or cannot be compared   | unchanged, because an account of the wrong type crashes Xray |
| An `x-usher` error, or a credential or client file cannot be built            | users who left `usher.yml` are removed; none are added   |
| A drift                                                                       | in step with `usher.yml`                                 |
