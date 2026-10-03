#!/usr/bin/env bash
# Builds, installs and loads the amneziawg kernel module at commit $AWG_MODULE_REF on the
# running kernel.
set -euo pipefail

src=$(mktemp -d)
git -C "$src" init --quiet
git -C "$src" fetch --quiet --depth 1 \
  https://github.com/amnezia-vpn/amneziawg-linux-kernel-module.git "$AWG_MODULE_REF"
git -C "$src" checkout --quiet FETCH_HEAD
version=$(sed -n 's/^#define [A-Z_]*VERSION "\([^"]*\)".*/\1/p' "$src/src/version.h" | head -n 1)

make -C "$src/src" WIREGUARD_VERSION="$version"
sudo make -C "$src/src" install WIREGUARD_VERSION="$version"
sudo depmod -a
sudo modprobe amneziawg

uname -r
cat /sys/module/amneziawg/version
