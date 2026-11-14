# WebOne-go

This is a Go port of [WebOne](https://github.com/atauenis/webone). WebOne
is an HTTP 1.x proxy that lets old web browsers, media players and
messengers operate again on the modern web. Old browsers ask the proxy
for `http://` pages. Then the proxy does these steps:

1. It gets the pages through modern HTTPS.
2. It changes the parts that old browsers cannot process.
3. It converts the text to a code page that the browsers can read.
4. It sends the pages back as HTTP/1.0 or 1.1.

D-Modem uses the proxy to let callers who dial into the BBS use the web
from old machines. The proxy can sign its HTTPS certificates with the CA
of the BBS. Thus callers must install only one certificate.

## Where this comes from

Alexander Tauenis and the WebOne Development Team wrote WebOne in C#,
under the BSD-style licence in [LICENSE.txt](LICENSE.txt). This repository
is a derivative work. It follows WebOne **0.19.0** at upstream commit
[`e940b43`](https://github.com/atauenis/webone/commit/e940b4304779f1f1a58d1e9df9500bdb6461e41e)
(22 October 2026). The Go code is a new translation of that C#, file by
file. For example, `HttpTransit.cs` became `internal/proxy/transit.go`,
and `ConfigFileLoader.cs` became `internal/config/loader.go`. The files
that follow are from WebOne. They are the same as upstream, except where
this list tells of a change:

* `LICENSE.txt`, unchanged. Its last clause applies to this port too: **all
  users of a service based on this software must be told that their traffic
  is changed, and about the security problems that brings.**
* `webone.conf`, `codepage.conf`, `escargot.conf`: the configuration and
  the sets of edits of WebOne. `webone.conf` has the options of the Go
  port, added as comments.
* `html/` (internal and error pages, icons) and `logo.webp`.
* `webone.service`, `webone.logrotate`. The service file does not have
  the `OPENSSL_CONF` line, which is only for .NET. It has a new note
  about how to run as the user of the BBS.

The [WebOne wiki](https://github.com/atauenis/webone/wiki) documents the
configuration. That documentation also applies to this port. The list
below gives the differences.

## Build and run

You must have Go 1.26 or later:

```sh
go build -o webone ./cmd/webone
./webone                       # finds webone.conf (here, next to the program, ~/.config/webone, /etc)
./webone 8080                  # on another port
./webone -cfg /etc/webone.conf -l /var/log/webone.log
./webone --help
```

Alternatively, use make:

```sh
make                  # builds ./webone
make test             # gofmt, vet and the tests
sudo make install     # see below
make dist GOOS=linux GOARCH=386   # a tarball for another machine
```

`make install` puts the program, `html/` and `logo.webp` in
`/usr/local/lib/webone`, and links `/usr/local/bin/webone` to the program.
It also installs these files:

- `/etc/webone.conf`, `/etc/webone.conf.d/codepage.conf` and
  `escargot.conf`. If one of these files is already there, it does not
  change it.
- The systemd unit and the logrotate rule.

`PREFIX`, `SYSCONFDIR` and `DESTDIR` change the locations of the files.
`make uninstall` removes all files except the configuration.

Put the `html/` directory next to the program, or where
`ContentDirectory=` points. Then do one of these steps:

- Set the HTTP proxy of the old browser to `host:8080`.
- Set the automatic configuration URL of the old browser to
  `http://host:8080/auto.pac`.
- For browsers without a proxy setting, use
  `http://host:8080/http://example.com/`.

To see the status page of the proxy, open `http://host:8080/`.

These features operate:

* All three request modes: as a proxy, as a web server for its own pages,
  and the `http://proxy/http://site/` alternate mode. CERN-style FTP is
  also supported.
* HTTP/1.0 and HTTP/1.1 clients, with keep-alive. The reply uses chunked
  transfer only for HTTP/1.1 clients.
* Requests to sites through HTTPS, HTTP/2 and HTTP/3 (refer to *HTTP/3 and gRPC*), with HTTP→HTTPS upgrades,
  `[ForceHttps]`, and the processing of redirect loops ("carousel").
* Sets of edits (`[Edit:...]`): all the conditions and actions of WebOne,
  with .NET regular expressions (through
  [regexp2](https://github.com/dlclark/regexp2)).
* Code pages: `OutputEncoding`, `[Translit]`, and `codepage.conf`. The
  port includes Windows, DOS, Mac, ISO and KOI8 code pages, UTF-16 and
  the CJK code pages.
* The HTTPS proxy (CONNECT): man-in-the-middle with a certificate for
  each site, for clients from SSL 2.0 to TLS 1.3 (refer to *Old SSL
  clients*). Also pass-through (`[ConnectPassThrough]`), non-HTTP tunnels
  (`[NonHttpSslServers]`, `[NonHttpConnectRedirect]`) and
  `[NonHttpSslDecrypt]`.
* The CA: made at the first start, or with `--rebuild-ca`. MD5, SHA-1 or
  SHA-2 signatures. External generators (`SslSiteCerts`,
  `SslSiteCertGenerator`). `/!ca` and `/weboneca.pem` serve the CA
  certificate.
* Internal pages: status, `/!codepages`, PAC/WPAD, `/!ftp/` (the web FTP
  client), `/!convert/` and on-the-fly converters (ImageMagick and other
  converters), `/!webvideo/` and `/!player/` (yt-dlp and ffmpeg),
  `/!redirect/`, `/!clear/`, and the `html/` pages and error pages.
* The Web Archive fallback, proxy logins, IP and URL lists, traffic dumps
  (`--dump`), ActiveX codebase emulation, and log files.

## Using the D-Modem BBS's CA

The BBS has a CA (refer to the *Certificates* wiki page of the BBS).
Callers install its root, `ca/ca.crt`, from `ca/install-ca.zip`. Make a
sub-CA for the proxy:

```sh
bbs ca subca proxy        # makes ca/proxy-ca.crt and ca/proxy-ca.key
```

Then set the directory in the configuration of the proxy:

```ini
[SecureProxy]
SslEnable=yes
SslCaDirectory=/var/lib/bbs/ca     ; the BBS's ca/ directory, beside its database
;SslHashAlgorithm=SHA1             ; leave out to sign as the sub-CA is signed

[Server]
DnsServer=127.0.0.1:53             ; optional: look sites up through the BBS's DNS
```

With `SslCaDirectory` set:

* The proxy signs the certificate of each site with `proxy-ca.key`, and
  sends `proxy-ca.crt` after it. Callers who installed the BBS root trust
  the two certificates.
* `/!ca` gives the root `ca.crt`. This is the certificate that callers
  install.
* If `SslHashAlgorithm` is not set, the proxy signs certificates with the
  algorithm of the sub-CA. Thus a CA made with `bbs ca init -sha1` gives
  SHA-1 certificates. Windows 95 to XP SP2 must have SHA-1 certificates.
* Site certificates are valid for a maximum of 397 days. The proxy caches
  them, and each subject keeps one serial number.
* The proxy never changes the files of the BBS. `--rebuild-ca` refuses to
  change them. If the directory has no `proxy-ca`, the proxy signs with
  the `ca.key` of the root, and writes a warning in the log. It is better
  to make the sub-CA.

**Permissions.** The BBS keeps `ca/` at mode 0700 and the keys at 0600. The
owner is the user in the `[general]` section of the BBS. Do one of these
steps:

- Run the proxy as that user (refer to `webone.service`).
- Give the user of the proxy read access to `ca/proxy-ca.key`,
  `ca/proxy-ca.crt` and `ca/ca.crt` (for example, through a shared
  group), and search access to `ca/`.

If the proxy cannot read these files, it tells you clearly.

SSL 2.0 sends only the certificate of the site, not the sub-CA after it.
Thus, if the browser of a caller can use only SSL 2.0, the caller must
install `ca/proxy-ca.crt` and the root.

`SslTrustOwnCA=yes` makes the proxy also trust that CA when it gets pages
itself. Use it for sites that the BBS serves under its own CA.

## Old SSL clients

The TLS of Go starts at TLS 1.0 and does not accept an SSL 2.0-format
hello. For those clients, the CONNECT/MITM mode uses
[retrossl](https://github.com/D-Modem/retrossl), the SSL 2.0, SSL 3.0 and
TLS 1.0 server of D-Modem. The proxy examines the first bytes of each
client:

* An SSL 2.0-format hello (Netscape 1 to 4, IE 2 to 6, AIM, early
  Opera), or a hello that offers a maximum of SSL 3.0, goes to retrossl.
* All other hellos go to the TLS of Go, as before.

`SslProtocols` sets the protocols that the proxy serves. `Ssl2` and
`Ssl3` enable SSL 2.0 and 3.0, and `Tls` enables TLS 1.0, for the two
servers. Without `SslProtocols`, SSL 3.0 and TLS 1.0 to 1.3 are enabled,
and SSL 2.0 is disabled. `SslCipherSuites` also accepts the suite names
of retrossl, for example `TLS_RSA_EXPORT_WITH_RC4_40_MD5`.

retrossl offers AES, 3DES, RC4, IDEA, RC2 and DES. It also offers the 40-
and 56-bit export suites for export builds of old browsers. Sessions
resume, thus slow machines do not do an RSA operation on each
connection. Clients that support secure renegotiation can renegotiate.
Site certificates must have RSA keys, and the certificates of the proxy
have RSA keys. It is easy to break these protocols. Thus, think of the
traffic between an old browser and the proxy as readable. This does not
apply to clients that use the plain HTTP proxy. For these clients, the
browser asks for `http://`, and the proxy gets the page through modern
HTTPS.

## HTTP/3 and gRPC

**HTTP/3.** The proxy gets sites through HTTP/3 (QUIC, through
[quic-go](https://github.com/quic-go/quic-go)) in the same way as
browsers. The first request to a site goes through TCP. If the site tells
in `Alt-Svc` that it has HTTP/3, the next requests to it use QUIC. If
QUIC fails (for example, if UDP is blocked), the site goes back to TCP
for ten minutes. The proxy immediately sends a request without a body
again through TCP. `RemoteHttpVersion` (and `AddRequestHttpVersion` in
edit sets) now also accepts `3.0`:

| Rule | Toward servers |
|---|---|
| `auto` (default), `>1.1` | HTTP/1.1, HTTP/2, and HTTP/3 after a site offers it |
| `=3.0` or `>2.0` | HTTP/3 only, immediately (fails if a site does not have HTTP/3) |
| `<3.0` | HTTP/1.1 and HTTP/2, never HTTP/3 |
| `=1.1`, `<2.0` | HTTP/1.1 only |

HTTP/3 must have TLS 1.3. Thus HTTP/3 is disabled if
`SecurityProtocols` does not include TLS 1.3. The proxy does not use
HTTP/3 through an `UpperProxy`, because QUIC cannot go through an HTTP
proxy. `DnsServer` applies to HTTP/3 in the same way as to TCP.

**gRPC.** gRPC must have HTTP/2 from end to end, with the trailers. Thus
it cannot go through the HTTP/1 processing of the proxy. It goes through
`CONNECT`, as gRPC clients do with a proxy
(`https_proxy=http://host:8080`):

* A CONNECT to port 443 that the proxy decrypts: if a client offers only
  `h2` in ALPN (as gRPC clients do), it gets HTTP/2 from the proxy. Its
  requests go to the site through HTTP/2 without changes. They stream in
  the two directions with their trailers. The URL lists still apply, but
  edit sets do not apply. Browsers also offer HTTP/1.1 (or no ALPN), thus
  the proxy processes them as before.
* `[NonHttpSslServers]` tunnels: the proxy now sends the ALPN request of
  the client to the server, and gives the answer of the server to the
  client. Thus gRPC (and all other protocols that must have ALPN)
  operates through these tunnels.
* `[ConnectPassThrough]` and plain non-HTTP tunnels send the bytes
  without changes. Thus gRPC already operated through them.

gRPC-Web runs on HTTP/1.1 and goes through the usual proxy.

## Differences from WebOne

### Not available in Go

* `UseMsHttpApi` (the HTTP.sys server of Windows) is not available. The
  proxy ignores it and writes a warning. The proxy always uses its own
  server, as WebOne does by default.
* The proxy accepts `MultipleHttp2Connections`, but Go itself controls
  this behaviour.

### Fixed on purpose

The port does not copy these bugs and small problems of WebOne 0.19.0:

* The proxy tests the `OnContentType` masks of edit sets against the
  content type of the response in the correct sequence.
* `%UrlNoDomain%`, `%UrlNoQuery%`, `%UrlNoPort%`, `%UrlHttp%` and
  `%UrlHttps%` do not make `//` or `??`.
* If there are many `Set-Cookie` headers, they go to the client as
  separate lines. The proxy does not join them with commas.
* The proxy sends `Connection: Keep-Alive` only to clients that keep
  connections. These are clients that ask for it, and HTTP/1.1 clients
  that do not send `close`. Other clients get `Connection: Close`. WebOne
  promises keep-alive to all clients and then closes the connection.
* Replies to `HEAD`, and `204`/`304` replies, have no body and no chunk
  terminator.
* The proxy accepts chunked request bodies (in WebOne, this function is
  only a stub).
* The proxy replaces `$Variable` names in the sequence of their length,
  longest first. Thus `$UA` does not cut `$UA-Firefox` short.
* `OpenForLocalIPs` finds which clients are "local" from the address
  (loopback, private, link-local). It does not ping the client with a TTL
  of 1.
* Error pages escape the values that they send to `html/Err-*.htm`. Thus
  a URL with `&` in it shows completely.
* The web FTP client reads multi-line FTP replies correctly, not with a
  timer. It accepts `125` and `150`. If a server behind NAT gives a
  private address for passive mode, the client uses the host of the
  control connection.
* Code pages round-trip: if a code page does not define some bytes, those
  bytes come back as the same bytes, as in .NET.
* `/!webvideo/` with `dump-json` returns the output of yt-dlp. WebOne
  returned its error stream.

### Tightened

* `/!convert/?src=` opens only files in the directory of the proxy (no
  paths). WebOne opens all paths that it gets.
* `/!webvideo/` does not accept yt-dlp options from the URL that read or
  write files or run programs (`exec`, `output`, `batch-file`,
  `config-locations`, `cookies`, `netrc-cmd` and similar options). These
  options still operate from `[WebVideoOptions]` in the configuration.
* If a converter hangs, the proxy stops it when the client disconnects.
  WebOne measures CPU use with Windows performance counters.

### Added (Go port only)

| Option | Section | What it does |
|---|---|---|
| `SslCaDirectory` | `[SecureProxy]` | sign with the CA of the D-Modem BBS (refer to the section above) |
| `SslTrustOwnCA` | `[SecureProxy]` | also trust the CA of the proxy for the sites that the proxy gets |
| `DnsServer` | `[Server]` | find the addresses of sites through this DNS server (`host` or `host:port`) |

`RemoteHttpVersion` also accepts `3.0` (refer to *HTTP/3 and gRPC*).

`SslProtocols` and `SslCipherSuites` operate as in WebOne. retrossl
serves SSL 2.0 and 3.0 (refer to the section above).

The proxy gives its version as `0.19.0-go`.

## Layout

```
cmd/webone          the program: command line, CA loading, start-up
internal/config     webone.conf: loading, [Include:], variables, sets of edits
internal/proxy      the server, request handling, CONNECT (old SSL through retrossl, HTTP/2 for gRPC), HTTP/3 fetching, internal pages, FTP, video
internal/certs      the CA, site certificates (MD5/SHA-1 signing), the BBS's CA
internal/textenc    code pages
internal/rx         .NET-style regular expressions, cached
internal/logx       the log, in WebOne's format
```

`go test ./...` tests these items:

- The configuration parser, edit sets and code pages.
- The certificates, with a BBS-style sub-CA chain.
- The proxy from end to end: HTTP/1.0 with edits and a code page, HTTPS
  requests, alternate mode, keep-alive with chunked replies, internal
  pages, CONNECT with MITM (with SSL 2.0 and 3.0 clients), gRPC-style
  HTTP/2 streams through CONNECT, HTTP/3 requests after Alt-Svc, and
  pass-through.

retrossl has its own tests.

## Licence

This port is under the same terms as WebOne: see [LICENSE.txt](LICENSE.txt).
Copyright (C) 2019+, WebOne Development Team. The Go port is copyright the
D-Modem project contributors.
