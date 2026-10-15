# Operations

This page covers what usher leaves to the services and how to issue new keys to a user.

## Isolation and DNS

usher does not configure the services. For AmneziaWG, the interface config does it: a `FORWARD`
drop between clients in `PostUp`, and a resolver on the tunnel address (for example cloudflared
with a DoH upstream in the awg-grpc network namespace) with `USHER_DNS` set to that address.

## New keys for a user

`state/users.json` has two groups: `awg` with entries `<interface>/<user>` and `xray` with entries
`<tag>/<user>`. usher never deletes an entry. To issue new keys:

1. `docker compose stop usher`
2. `docker run --rm -it -u 1000:1000 -v <project>_usher-state:/state alpine vi /state/users.json`
3. Delete the entries of the user, then `docker compose start usher`

A user still in `usher.yml` gets new keys, and every client file of that user changes.
