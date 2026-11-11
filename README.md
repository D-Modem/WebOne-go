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
  comments, and `SslProtocols` no longer lists SSL 2.0 and 3.0 (see below).
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
* Fetching over HTTPS (and HTTP/2), with HTTP→HTTPS upgrades,
  `[ForceHttps]`, and redirect loop ("carousel") handling.
* Sets of edits (`[Edit:...]`): every condition and action WebOne has, .NET
  regular expressions included (through
  [regexp2](https://github.com/dlclark/regexp2)).
* Code pages: `OutputEncoding`, `[Translit]`, and `codepage.conf`. Windows,
  DOS, Mac, ISO and KOI8 code pages are included, as are UTF-16 and the CJK
  ones.
* The HTTPS proxy (CONNECT): man-in-the-middle with a per-site
  certificate, pass-through (`[ConnectPassThrough]`), non-HTTP tunnels
  (`[NonHttpSslServers]`, `[NonHttpConnectRedirect]`) and `[NonHttpSslDecrypt]`.
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

`SslTrustOwnCA=yes` makes the proxy also trust that CA when it fetches pages
itself. Use it for sites the BBS serves under its own CA.

## Differences from WebOne

### Not available in Go

* **No SSL 2.0 or 3.0, and no SSLv2-format hello.** Go's TLS starts at
  TLS 1.0. Browsers that only speak SSL (Netscape 1 to 4, IE 2 to 5 with its
  defaults) cannot use the CONNECT/MITM mode. They work fine through the
  plain HTTP proxy, which is WebOne's main mode anyway: the browser asks for
  `http://`, and the proxy does the HTTPS. `Ssl2` and `Ssl3` in
  `SslProtocols` are ignored with a warning. Clients that need TLS 1.0 with
  old ciphers (IE 6 to 8 on XP) do work: all of Go's suites, the weak ones
  included, are offered unless `SslCipherSuites` says otherwise. Supporting
  SSL 3.0 is a possible follow-up.
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

The version is reported as `0.19.0-go`.

## Layout

```
cmd/webone          the program: command line, CA loading, start-up
internal/config     webone.conf: loading, [Include:], variables, sets of edits
internal/proxy      the server, request handling, CONNECT, internal pages, FTP, video
internal/certs      the CA, site certificates (MD5/SHA-1 signing), the BBS's CA
internal/textenc    code pages
internal/rx         .NET-style regular expressions, cached
internal/logx       the log, in WebOne's format
```

`go test ./...` covers configuration parsing, edit sets, code pages, the
certificates (including a BBS-style sub-CA chain), and the proxy end to end:
HTTP/1.0 with edits and a code page, HTTPS fetching, alternate mode,
keep-alive with chunked replies, internal pages, CONNECT with MITM, and
pass-through.

## Licence

This port is under the same terms as WebOne: see [LICENSE.txt](LICENSE.txt).
Copyright (C) 2019+, WebOne Development Team. The Go port is copyright the
D-Modem project contributors.
