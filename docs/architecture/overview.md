# usher overview

## Purpose

usher keeps AmneziaWG peers, Xray users and client files in step with `config/usher.yml`, a map of
user name to interface names. An interface is an AmneziaWG interface or a tagged inbound of the
operator's Xray config. The operator types names only. Addresses and keys are internal.

## Stack

Go, one binary: `usher run`. It talks gRPC to `awg-grpc` (`github.com/mrcsin/awg-grpc` v0.1.0)
over the socket in `USHER_AWG_SOCKET`, and to Xray's `HandlerService` over the socket in
`USHER_XRAY_SOCKET`. At least one is set. It runs in a container with no capabilities, a read-only
root file system and uid 1000. Config parsing uses `gopkg.in/yaml.v3`. The Xray backend imports
`xray-core` (`infra/conf`, `app/proxyman/command`) and `libxray/share`.

## Components

| Package | Responsibility |
|---|---|
| `cmd/usher` | Reads the `USHER_*` environment, builds the backends, wires signals, the watcher and the pass. |
| `internal/config` | Loads and validates `usher.yml`. |
| `internal/state` | Loads and saves `state/users.json`, version 2. |
| `internal/atomicfile` | Replaces a file atomically for `state` and the `clients/` mirror. |
| `internal/pass` | Settings, the `Backend` and `Session` seam and the reconcile pass. |
| `internal/awg` | The awg backend: enrollment, address allocation, `.conf` rendering. |
| `internal/xray` | The Xray backend: config reader, drift guard, users, mirror, `.json` and `.txt` rendering, stale socket recovery. |
| `internal/clients` | Paths of client files and the mirror of the `clients/` directory. |
| `internal/watch` | Polls the file, runs the refill ticker and serializes passes. |

## The pass

A backend is a `Backend{Name, Suffixes, Open}`. `Open` returns a `Session` with `Interfaces`,
`Prepare` (enroll users, render files), `Apply` and `Close`. `Suffixes` lists the file suffixes
the backend owns (`.conf`; `.json` and `.txt`). A backend that fails to open is down for the pass:
usher keeps every file with one of its suffixes. An interface name that two backends report fails
in both. The steps below run per backend; the text names the awg calls.

1. Load `usher.yml`. When it is invalid, use the last valid file of this process. With none,
   log and stop. Load `users.json`. On error, log and stop.
2. Call `GetStatus`. On error, log and stop.
3. For each reported interface: fail it when `present=false`. Otherwise enroll every user that
   references it and render their configs. An enrollment or render error fails the interface.
4. Save `users.json` when step 3 created or changed an entry.
5. For each interface that did not fail: call `ApplyPeers` with one peer per referencing user
   (`address/32`), with `allow_empty` when no user references the interface. An error fails the
   interface.
6. When no backend is down, log every user and interface pair that no backend reports ("interface
   is not reported by any backend").
7. Make `clients/` equal to the rendered files of applied interfaces plus the current files of
   failed ones.

Two triggers start a pass: a change of `usher.yml` and a 30 s refill ticker. One goroutine runs
the watcher loop and the passes, with the poll ticker, the settle timer and the refill ticker as
cases of one `select`. A tick that fires during a pass waits in its ticker, which buffers one tick,
so each source yields at most one following pass. A refill tick and a file change during one pass
give two following passes. File polling follows `reproxy` (`app/discovery/provider/file.go`): `os.Stat` every 3 s, and a new
modification time counts once it has stayed unchanged for 500 ms. The connection is opened per
pass and every call has a 10 s deadline.

## Keys and addresses

- `state/users.json` is the only source of secrets. Entries are keyed `interface/user` under the
  `awg` group. The kernel and `clients/` derive from it. usher saves it (temp file, fsync, rename,
  directory fsync, mode 0600) only when an entry changed, and always before any backend's `Apply`.
- A new entry gets an X25519 key pair (`crypto/ecdh`), 32 random PSK bytes and an address.
- The address is the lowest host address of the interface's first IPv4 prefix that is not the
  network or broadcast address, not an interface address and not held by another entry of that
  interface. A stored address that breaks one of these rules, for example after the operator
  changes the interface address, is reassigned and logged, and the config is rewritten.
- Switching a user off removes the peer and the client file and keeps the entry, so the same
  name gets the same key and address back. usher never deletes entries.

## Locked decisions

- The scope is AmneziaWG over IPv4 and the Xray shapes in the README.
- usher owns every interface the wrapper reports, and an unreferenced interface gets an empty
  list.
- A broken `usher.yml` never changes state. The last valid file is kept in memory only.
- A file without any user and interface pair is invalid, so a truncated file cannot wipe a node.
- A failure of one interface never stops the daemon and never blocks the others.
- An interface without an IPv4 prefix fails only when a user references it.
- `clients/` files of an interface that no backend reports are removed.
- `USHER_HOST` is an IPv4 address, not a name: a client whose DNS points into the tunnel cannot
  resolve the name again to reconnect.
- No private key, PSK or `client_params` value is logged. A public key without an entry is logged
  as base64.
- No Reality `privateKey` or `mldsa65Seed`, key file, `sockopt`, `fallbacks` entry or drift value
  is logged or written to `clients/`.
- No subcommands besides `usher run`, no central management, no migration of existing peers.

## Operations

- `clients/` belongs to usher: anything not rendered from the applied state is removed. Files are
  mode `0600`, uid 1000.
- Changing an interface's address, key, port or obfuscation values rewrites every client config of
  that interface; those configs are handed out again.
- Addresses are never freed: switched-off users keep theirs. A full subnet fails the whole
  interface, refill included. awg-grpc accepts at most 1000 peers per interface.
- `users.json` sits in the `usher-state` volume (mode `0600`, uid 1000). To re-key or delete a user:
  `docker compose stop usher`, then
  `docker run --rm -it -u 1000:1000 -v <project>_usher-state:/state alpine vi /state/users.json`,
  delete the `<interface>/<user>` entry, `docker compose start usher`. A user still in `usher.yml`
  gets new keys.
- `USHER_HOST` is an address, not a name: a client that re-resolves a name while its DNS points
  into the tunnel cannot reconnect.
- Client isolation and DNS redirection belong to the service config, not to usher. For AmneziaWG:
  `FORWARD` drops between clients in `PostUp`; a resolver on the tunnel address (for example
  cloudflared with a DoH upstream in the `awg` network namespace) with `USHER_DNS` set to that
  address, and optionally a `DNAT` of port 53 in `PostUp`.

## Xray backend

### Ownership

- The operator owns one Xray server config: inbounds, outbounds, routing, `api`, certificates,
  Reality keys, and a per-inbound `x-usher` block with client hints. Xray reads it at start. usher
  reads the same file read-only at every pass.
- usher owns the users inside the inbounds, through the API, and everything under `clients/`.
  Xray keeps users in memory, so the refill tick restores them after a restart.
- A managed inbound has a tag that matches the interface name pattern (32 characters), one integer
  port, no unix `listen`, empty `clients` and a protocol of vless, hysteria, vmess or trojan.
  Other inbounds are skipped. An inbound that fails validation carries its error. `Prepare` keeps
  that error (as it does for an enroll or render error) and returns none, so the pass still calls
  `Apply`. `Apply` then only removes the users who left, and returns the error, which keeps the old
  files. A tag defined twice is reported once, and its entry carries the error.

### Reading the config

`ReadConfig` reads the file with Xray's comment-aware reader into `infra/conf` types plus raw
maps. It normalizes the aliases (`method`, `splithttp`, `websocket`, `tcp`, `splithttpSettings`,
`tcpSettings`, Hysteria `users`) before validation and mirroring. Every error names the field and
never a value. `x-usher` decodes with unknown keys rejected. Reality matches names by exact lookup,
so there an explicit `x-usher.sni` must be one of the `serverNames` entries, and the config needs
an entry without a wildcard when `sni` is unset. A Reality `privateKey` and `mldsa65Seed` must
decode, and the seed must differ from the key. `hop_ports` requires from < to. An inbound without
`streamSettings` stays without them in `Raw`, so it builds as Xray builds the file. ReadConfig registers a discard handler
for Xray's global logger once, because `Build()` prints deprecation warnings.

### Users

- The email is `tag/user`, level 0. State keeps `id` (UUID v4) for VLESS and VMess, `password`
  (base64url of 32 random bytes) for Trojan and Hysteria2, under the `xray` group. enroll never
  changes an existing entry.
- `Apply` lists all users of the inbound with `GetInboundUsers` and compares the credential only:
  VLESS `id` and `flow`, VMess `id`, Trojan `password`, Hysteria `auth`. It removes foreign and
  changed emails and adds missing and changed ones. It tries every operation, joins the errors and
  logs `users changed`. The next pass converges from the listed state.
- When the running inbound is missing or runs another protocol, `Apply` changes nothing: an
  account of the wrong type makes Xray's validator panic and kills the process.
- Removal stops new connections. An open Hysteria2 QUIC connection stays until the client
  reconnects.

### Drift guard

`Open` calls `ListInbounds` once and compares each valid inbound with `Build()` of its file entry:
proxy type, proxy settings, port, stream settings and receiver settings. Certificate entries that
name a file are dropped on both sides, because Xray reloads those files itself. A tag missing at
runtime counts as drift. The reason names the differing parts; an error of `Build()` becomes a fixed
text, because Xray quotes the keys and short ids it rejects. `Apply` reconciles users first and then
returns the drift error, so revocation works while the old client files stay. The exception is a
missing inbound or another proxy type (see Users). A running managed inbound that the file does not define is logged each pass.

### Stale socket

A killed Xray leaves its API socket, and a new start fails on it. Xray holds `flock(LOCK_EX)` on
`<socket>.lock` while it listens. Before dialing, usher opens the lock read-only and tries
`LOCK_EX|LOCK_NB`. When it gets the lock, no Xray listens: usher unlinks the socket and reports
Xray down for this pass. The restart policy starts Xray at its next attempt. The socket directory
is a bind mount that usher can write. A failed Xray start removes the lock file, so for a socket without
one usher probes the socket instead: only a refused connection counts as stale, and a live Xray, or
a socket that a new Xray replaced meanwhile, stays. A path that is not a socket is an error.

### Client mirror

- Taken from the server: `network`, `security`, path and host of ws and httpupgrade,
  `serviceName` and `authority` of grpc. Everything else of the stream settings is dropped.
- xhttp: path, host and mode stay at the top. A server `extra` is copied verbatim; otherwise every
  other field moves into `extra`. `.json` and `.txt` carry the same form. `downloadSettings` fails
  the inbound.
- VLESS `flow` is `xtls-rprx-vision` when the network is raw and the security is tls or reality.
- `tlsSettings` is rebuilt: server name, ALPN, fingerprint and, with `trust: pin`,
  `pinnedPeerCertSha256`.
- `realitySettings` is rebuilt: public key from `privateKey` (`crypto/ecdh`), `shortIds[0]`,
  `spiderX` `/`, `mldsa65Verify` from `mldsa65Seed`. The fingerprint is `chrome`, `firefox` or
  `safari`.
- Hysteria2: the client `auth` is the user's password. From salamander only `password` is copied;
  `packetSize` fails the inbound. `hop_ports` adds a client `udphop` mask (mode
  `intervalLocal,intervalRemote`, interval 30, in libxray's shape). The range needs a host DNAT to
  the inbound port.

### Outputs

- `clients/<user>/<tag>.json` is a complete client config: the outbound plus a socks inbound on
  127.0.0.1:10808 and an http inbound on 127.0.0.1:10809. The outbound must pass
  `conf.OutboundDetourConfig.Build()` before any file is written, or the inbound fails.
- `clients/<user>/<tag>.txt` comes from libxray `ConvertXrayJsonToShareLinks`. usher builds only
  shapes libxray knows, so a refusal is a usher defect: it is logged with the tag and no `.txt` is
  written. A Hysteria2 inbound with `trust: pin` gets no `.txt`.

## State

`state/users.json` is version 2: the `awg` group keyed `interface/user` and the `xray` group keyed
`tag/user`. An `xray` entry holds exactly one of `id` and `password`. `Load` accepts versions 1
and 2. Before the first save of a version 1 file usher writes `users.json.v1` beside it, so a
rollback is a file rename.
