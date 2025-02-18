# JitakuDNS

JitakuDNS is a simple and lightweight DNS server designed to be run on a home network.

Jitaku (自宅) is Japanese for "one's home".

## Features

- Ad block filter list support
- Hosts file style rewrites
- Single fully-static binary
- File-based configuration
- Minimalistic web interface
- Web-based dig tool for debugging
- Web-based configuration editor

## Non-Features

- No webui authentication
- No DHCP server

## To Do List

- Pretty graphs on the dashboard
- Ship Raspberry Pi appliance images (using gokrazy)
  - Stretch goal: one-click update pi image from web interface (also downgrading)
- Speed up adblock filter list processing
- Cache DNS responses

## Building

```sh
# Build all target images
make
```

## Development

```sh
export GOKRAZY_HOSTNAME=192.168.1.123
export GOKRAZY_PASSWORD=asdf

# Build image and flash to an sd card
make overwrite OVERWRITE_DEVICE=/dev/sdb

# Build image and update an existing installation remotely
make update

# Build server and run an existing installation remotely
make run
```
