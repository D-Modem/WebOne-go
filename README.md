# WebOne-go

WebOne-go is a Go port of [WebOne](https://github.com/atauenis/webone), an
HTTP 1.x proxy for old web browsers, media players and messengers. An old
browser asks the proxy for an `http://` page. The proxy gets the page
through modern HTTPS, changes the parts that the browser cannot process,
converts the text to a code page that the browser can read, and sends the
page back as HTTP/1.0 or 1.1.

D-Modem uses the proxy so that callers who dial into the BBS can use the
web from old machines. The proxy can sign its HTTPS certificates with the
CA of the BBS. Thus callers must install only one certificate.

- All the request modes, sets of edits, code pages and internal pages of WebOne 0.19.0
- An HTTPS proxy for clients from SSL 2.0 to TLS 1.3, through [retrossl](https://github.com/D-Modem/retrossl)
- Requests to sites through HTTP/2 and HTTP/3, and gRPC through `CONNECT`
- Signing with the CA of the D-Modem BBS

## Quick start

You must have Go 1.26 or later:

```sh
make                    # builds ./webone
./webone                # port 8080, with webone.conf from here, ~/.config/webone or /etc
sudo make install       # /usr/local/lib/webone, /etc/webone.conf and the systemd unit
```

Then set the HTTP proxy of the old browser to `host:8080`. To see the
status page of the proxy, open `http://host:8080/`.

## Documentation

The [WebOne wiki](https://github.com/atauenis/webone/wiki) documents the
configuration. That documentation also applies to this port.
[docs/](docs/README.md) gives the parts that are different in the Go port.

## Licence

This port is under the same terms as WebOne: see [LICENSE.txt](LICENSE.txt).
Copyright (C) 2019+, WebOne Development Team. The Go port is copyright the
D-Modem project contributors. The last clause of the licence applies to
this port too: **all users of a service based on this software must be
told that their traffic is changed, and about the security problems that
brings.**
