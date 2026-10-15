# Architecture

- [overview.md](overview.md): purpose, components, the pass, keys and addresses, the Xray backend,
  state.

## Locked decisions

| Area | Decision |
|---|---|
| Scope | AmneziaWG over IPv4 and the Xray shapes in [docs/xray.md](../xray.md#supported-shapes). No subcommands besides `usher run`, no central management, no migration of existing peers. |
| Ownership | usher owns the peers of every interface awg-grpc reports and the users of every managed Xray inbound; an unreferenced interface gets an empty list. It never writes the Xray config. |
| Broken `usher.yml` | Never changes state. The last valid file is kept in memory only. A file without any user and interface pair is invalid, so a truncated file cannot wipe a node. |
| Failures | A failure of one interface never stops the daemon and never blocks the others. An interface without an IPv4 prefix fails only when a user references it. |
| `clients/` | Files of an interface that no backend reports are removed, unless a backend is down. |
| `USHER_HOST` | An IPv4 address, not a name: a client whose DNS points into the tunnel cannot resolve the name again to reconnect. |
| Secrets | `state/users.json` is the only source of secrets. No private key, PSK or `client_params` value reaches a log line or an error message. No Reality `privateKey` or `mldsa65Seed`, key file, PEM key, `sockopt`, `fallbacks` entry or drift value reaches `clients/`, a log line or an error message; a drift reason names the differing part only. A public key without an entry is logged as base64. |

## Decision records

None.
