# WebOne-go

A Go port of [WebOne](https://github.com/atauenis/webone), the HTTP 1.x proxy
that makes old web browsers, media players and messengers usable again on
today's web. Old browsers ask the proxy for `http://` pages. The proxy fetches
them over modern HTTPS, patches what old browsers choke on, converts the text
to a code page they can read, and sends it back as HTTP/1.0 or 1.1.

D-Modem uses it so that callers dialling into the BBS can browse the web from
period machines. The proxy can sign its HTTPS certificates with the BBS's own
CA, so callers only have to install one certificate.

## Where this comes from

WebOne is written in C# by Alexander Tauenis and the WebOne Development Team,
under the BSD-style licence in [LICENSE.txt](LICENSE.txt). This repository is
a derivative work. It follows WebOne **0.19.0** at upstream commit
[`e940b43`](https://github.com/atauenis/webone/commit/e940b4304779f1f1a58d1e9df9500bdb6461e41e)
(22 October 2026). The Go code is a new translation of that C#, file by file:
`HttpTransit.cs` became `internal/proxy/transit.go`, `ConfigFileLoader.cs`
became `internal/config/loader.go`, and so on. The files below are WebOne's
own and are kept as upstream has them, except where noted:

* `LICENSE.txt`, unchanged. Its last clause applies to this port too: **all
  users of a service based on this software must be told that their traffic
  is changed, and about the security problems that brings.**
* `webone.conf`, `codepage.conf`, `escargot.conf`: WebOne's configuration
  and sets of edits. `webone.conf` has the Go port's options added as
  comments.
* `html/` (internal and error pages, icons) and `logo.webp`.
* `webone.service`, `webone.logrotate`. The service file drops the .NET-only
  `OPENSSL_CONF` line and gains a note about running as the BBS's user.

The [WebOne wiki](https://github.com/atauenis/webone/wiki) documents the
configuration, and that documentation holds for this port. The differences
are listed below.

## Build and run

Go 1.26 or later:

```sh
go build -o webone ./cmd/webone
./webone                       # finds webone.conf (here, next to the program, ~/.config/webone, /etc)
./webone 8080                  # on another port
./webone -cfg /etc/webone.conf -l /var/log/webone.log
./webone --help
```

Or with make:

```sh
make                  # builds ./webone
make test             # gofmt, vet and the tests
sudo make install     # see below
make dist GOOS=linux GOARCH=386   # a tarball for another machine
```

`make install` puts the program, `html/` and `logo.webp` in
`/usr/local/lib/webone` and links `/usr/local/bin/webone` to it. It also
installs `/etc/webone.conf`, `/etc/webone.conf.d/codepage.conf` and
`escargot.conf` (leaving any that are already there), the systemd unit and
the logrotate rule. `PREFIX`, `SYSCONFDIR` and `DESTDIR` change where things
go, and `make uninstall` removes everything but the configuration.

The `html/` directory goes next to the program, or wherever `ContentDirectory=`
points. Then set the old browser's HTTP proxy to `host:8080`, or its automatic
configuration URL to `http://host:8080/auto.pac`. Browsers with no proxy
setting can use `http://host:8080/http://example.com/`. Open
`http://host:8080/` to see the proxy's status page.

What works:

* All three ways of asking: as a proxy, as a web server for its own pages,
  and the `http://proxy/http://site/` alternate mode. CERN-style FTP is
  supported too.
* HTTP/1.0 and HTTP/1.1 clients, with keep-alive. The reply uses chunked
  transfer only for HTTP/1.1 clients.
* Fetching over HTTPS, HTTP/2 and HTTP/3 (see *HTTP/3 and gRPC*), with HTTP→HTTPS upgrades,
  `[ForceHttps]`, and redirect loop ("carousel") handling.
* Sets of edits (`[Edit:...]`): every condition and action WebOne has, .NET
  regular expressions included (through
  [regexp2](https://github.com/dlclark/regexp2)).
* Code pages: `OutputEncoding`, `[Translit]`, and `codepage.conf`. Windows,
  DOS, Mac, ISO and KOI8 code pages are included, as are UTF-16 and the CJK
  ones.
* The HTTPS proxy (CONNECT): man-in-the-middle with a per-site
  certificate, for clients from SSL 2.0 to TLS 1.3 (see *Old SSL
  clients*), pass-through (`[ConnectPassThrough]`), non-HTTP tunnels
  (`[NonHttpSslServers]`, `[NonHttpConnectRedirect]`) and
  `[NonHttpSslDecrypt]`.
* The CA: made on first start, or `--rebuild-ca`. MD5, SHA-1 or SHA-2
  signatures. External generators (`SslSiteCerts`, `SslSiteCertGenerator`).
  `/!ca` and `/weboneca.pem` serve the CA certificate.
* Internal pages: status, `/!codepages`, PAC/WPAD, `/!ftp/` (the web FTP
  client), `/!convert/` and on-the-fly converters (ImageMagick and others),
  `/!webvideo/` and `/!player/` (yt-dlp and ffmpeg), `/!redirect/`,
  `/!clear/`, and the `html/` pages and error pages.
* The Web Archive fallback, proxy logins, IP and URL lists, traffic dumps
  (`--dump`), ActiveX codebase emulation, and log files.

## Using the D-Modem BBS's CA

The BBS keeps a CA (see the BBS's *Certificates* wiki page). Callers install
its root, `ca/ca.crt`, from `ca/install-ca.zip`. Give the proxy a sub-CA of
its own:

```sh
bbs ca subca proxy        # makes ca/proxy-ca.crt and ca/proxy-ca.key
```

and point the proxy at the directory:

```ini
[SecureProxy]
SslEnable=yes
SslCaDirectory=/var/lib/bbs/ca     ; the BBS's ca/ directory, beside its database
;SslHashAlgorithm=SHA1             ; leave out to sign as the sub-CA is signed

[Server]
DnsServer=127.0.0.1:53             ; optional: look sites up through the BBS's DNS
```

With `SslCaDirectory` set:

* The proxy signs each site's certificate with `proxy-ca.key`, and sends
  `proxy-ca.crt` after it. Callers who installed the BBS root trust both.
* `/!ca` hands out the root `ca.crt`, which is the one callers install.
* Unless `SslHashAlgorithm` is set, certificates are signed with the
  sub-CA's own algorithm. A CA made with `bbs ca init -sha1` therefore gives
  SHA-1 certificates, which Windows 95 to XP SP2 need.
* Site certificates last at most 397 days. They are cached, and each subject
  keeps one serial number.
* The BBS's files are never changed, and `--rebuild-ca` refuses to touch them.
  A directory with no `proxy-ca` falls back to signing with the root's
  `ca.key`, and logs a warning. Making the sub-CA is the better way.

**Permissions.** The BBS keeps `ca/` at mode 0700 and the keys at 0600, owned
by the user in its `[general]` section. Either run the proxy as that user
(see `webone.service`), or give the proxy's user read access to
`ca/proxy-ca.key`, `ca/proxy-ca.crt` and `ca/ca.crt` (for example, through a
shared group), plus search access to `ca/`. The proxy says plainly when it
cannot read them.

SSL 2.0 sends only the site's own certificate, not the sub-CA after it. So
callers whose browsers speak only SSL 2.0 must install `ca/proxy-ca.crt` as
well as the root.

`SslTrustOwnCA=yes` makes the proxy also trust that CA when it fetches pages
itself. Use it for sites the BBS serves under its own CA.

## Old SSL clients

Go's own TLS starts at TLS 1.0 and does not accept an SSL 2.0-format hello.
For those clients, the CONNECT/MITM mode uses
[retrossl](https://github.com/D-Modem/retrossl), D-Modem's SSL 2.0, SSL 3.0
and TLS 1.0 server. The proxy looks at each client's first bytes:

* An SSL 2.0-format hello (Netscape 1 to 4, IE 2 to 6, AIM, early Opera), or
  a hello that offers SSL 3.0 at best, goes to retrossl.
* Everything else goes to Go's TLS, as before.

`SslProtocols` decides what is served: `Ssl2` and `Ssl3` turn on SSL 2.0 and
3.0, and `Tls` turns on TLS 1.0, for both servers. Without `SslProtocols`,
SSL 3.0 and TLS 1.0 to 1.3 are on, and SSL 2.0 is off. `SslCipherSuites`
also accepts retrossl's suite names, such as `TLS_RSA_EXPORT_WITH_RC4_40_MD5`.

retrossl offers AES, 3DES, RC4, IDEA, RC2 and DES, and the 40- and 56-bit
export suites for export builds of old browsers. Sessions resume, which
saves slow machines an RSA operation on every connection, and clients that
support secure renegotiation may renegotiate. Site certificates must have RSA keys,
which the proxy's do. These protocols are easy to break, so the traffic
between an old browser and the proxy should be treated as readable. Clients
that use the plain HTTP proxy instead are not affected: the browser asks for
`http://`, and the proxy fetches over modern HTTPS.

## HTTP/3 and gRPC

**HTTP/3.** The proxy fetches sites over HTTP/3 (QUIC, through
[quic-go](https://github.com/quic-go/quic-go)) the way browsers do: a site
is first reached over TCP, and once it says in `Alt-Svc` that it has HTTP/3,
later requests to it use QUIC. If QUIC fails (UDP blocked, say), the site
goes back to TCP for ten minutes, and a request without a body is retried
there at once. `RemoteHttpVersion` (and `AddRequestHttpVersion` in edit
sets) now takes `3.0` too:

| Rule | Toward servers |
|---|---|
| `auto` (default), `>1.1` | HTTP/1.1, HTTP/2, and HTTP/3 once a site offers it |
| `=3.0` or `>2.0` | HTTP/3 only, straight away (fails where a site has none) |
| `<3.0` | HTTP/1.1 and HTTP/2, never HTTP/3 |
| `=1.1`, `<2.0` | HTTP/1.1 only |

HTTP/3 needs TLS 1.3, so it is off when `SecurityProtocols` leaves TLS 1.3
out, and it is not used through an `UpperProxy` (QUIC can't go through an
HTTP proxy). `DnsServer` applies to it as to TCP.

**gRPC.** gRPC needs HTTP/2 from end to end, trailers included, so it can't
go through the proxy's HTTP/1 handling. It goes through `CONNECT` instead,
which is what gRPC clients do with a proxy (`https_proxy=http://host:8080`):

* A CONNECT to port 443 that the proxy decrypts: a client that offers only
  `h2` in ALPN (as gRPC clients do) gets HTTP/2 from the proxy, and its
  requests go on to the site over HTTP/2 untouched, streamed both ways with
  their trailers. The URL lists still apply; edit sets do not. Browsers
  offer HTTP/1.1 too (or no ALPN at all), so they are handled as before.
* `[NonHttpSslServers]` tunnels: the proxy now asks the server for what the
  client asked for in ALPN and gives the client the server's answer, so
  gRPC (and anything else that needs ALPN) works through them.
* `[ConnectPassThrough]` and plain non-HTTP tunnels pass bytes as they are,
  so gRPC already worked there.

gRPC-Web runs over HTTP/1.1 and goes through the ordinary proxy.

## Differences from WebOne

### Not available in Go

* `UseMsHttpApi` (Windows' HTTP.sys server) does not exist here. It is
  ignored with a warning, and the proxy always uses its own server, as
  WebOne does by default.
* `MultipleHttp2Connections` is accepted, but Go decides this itself.

### Fixed on purpose

These are bugs or rough edges in WebOne 0.19.0 that the port does not copy:

* Edit sets' `OnContentType` masks are tested against the response's content
  type in the right order.
* `%UrlNoDomain%`, `%UrlNoQuery%`, `%UrlNoPort%`, `%UrlHttp%` and
  `%UrlHttps%` no longer produce `//` or `??`.
* Several `Set-Cookie` headers reach the client as separate lines, instead
  of joined with commas.
* `Connection: Keep-Alive` is sent only to clients that keep connections:
  ones that ask for it, and HTTP/1.1 ones unless they say `close`. Other
  clients get `Connection: Close`. WebOne promises keep-alive to everyone and
  then closes.
* Replies to `HEAD`, and `204`/`304` replies, have no body or chunk
  terminator.
* Request bodies sent chunked are accepted (WebOne has this stubbed out).
* `$Variable` names are replaced longest first, so `$UA-Firefox` is not cut
  short by `$UA`.
* `OpenForLocalIPs` decides what is "local" from the address (loopback,
  private, link-local) instead of pinging the client with a TTL of 1.
* Error pages escape what they pass to `html/Err-*.htm`, so a URL with `&` in
  it is shown whole.
* The web FTP client reads multi-line FTP replies properly instead of timing
  them. It accepts `125` as well as `150`, and falls back to the control
  connection's host when a server behind NAT gives a private address for
  passive mode.
* Code pages round-trip: bytes a code page leaves undefined come back as the
  same bytes, as .NET does.
* `/!webvideo/` with `dump-json` returns yt-dlp's output, where WebOne
  returned its error stream.

### Tightened

* `/!convert/?src=` opens only files in the proxy's own directory (no paths).
  WebOne opens any path it is given.
* `/!webvideo/` does not take yt-dlp options that read or write files or run
  programs (`exec`, `output`, `batch-file`, `config-locations`, `cookies`,
  `netrc-cmd` and similar) from the URL. They still work from
  `[WebVideoOptions]` in the configuration.
* Converters that hang are stopped when the client goes away. WebOne instead
  measures CPU use with Windows performance counters.

### Added (Go port only)

| Option | Section | What it does |
|---|---|---|
| `SslCaDirectory` | `[SecureProxy]` | sign with the D-Modem BBS's CA (above) |
| `SslTrustOwnCA` | `[SecureProxy]` | trust the proxy's CA for fetched sites too |
| `DnsServer` | `[Server]` | look sites up through this DNS server (`host` or `host:port`) |

`RemoteHttpVersion` also takes `3.0` (see *HTTP/3 and gRPC*).

`SslProtocols` and `SslCipherSuites` work as in WebOne, with SSL 2.0 and 3.0
served by retrossl (above).

The version is reported as `0.19.0-go`.

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

`go test ./...` covers configuration parsing, edit sets, code pages, the
certificates (including a BBS-style sub-CA chain), and the proxy end to end:
HTTP/1.0 with edits and a code page, HTTPS fetching, alternate mode,
keep-alive with chunked replies, internal pages, CONNECT with MITM (SSL
2.0 and 3.0 clients included), gRPC-style HTTP/2 streams through CONNECT,
HTTP/3 fetching after Alt-Svc, and pass-through. retrossl has its own tests.

## Licence

This port is under the same terms as WebOne: see [LICENSE.txt](LICENSE.txt).
Copyright (C) 2019+, WebOne Development Team. The Go port is copyright the
D-Modem project contributors.
