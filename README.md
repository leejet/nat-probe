# NAT Probe using STUN

## Usage

```sh
$ go get github.com/leejet/nat-probe
$ $GOPATH/bin/nat-probe
```

```sh
Usage of nat-probe:
  -b string
        bind UDP socket to a specific local address (default "0.0.0.0")
  -s string
        STUN server address (default "stun.syncthing.net:3478")
  -t int
        the number of seconds to wait for STUN server's response (default 3)
  -v    verbose
```

## Build

```
go mod tidy
go build
```

## Credits
- [pion/stun](https://github.com/pion/stun) - A Go implementation of STUN