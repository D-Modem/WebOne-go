# Differences from WebOne

## Not available in Go

* `UseMsHttpApi` (the HTTP.sys server of Windows) is not available. The
  proxy ignores it and writes a warning. The proxy always uses its own
  server, as WebOne does by default.
* The proxy accepts `MultipleHttp2Connections`, but Go itself controls
  this behaviour.

## Fixed on purpose

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

## Tightened

* `/!convert/?src=` opens only files in the directory of the proxy (no
  paths). WebOne opens all paths that it gets.
* `/!webvideo/` does not accept yt-dlp options from the URL that read or
  write files or run programs (`exec`, `output`, `batch-file`,
  `config-locations`, `cookies`, `netrc-cmd` and similar options). These
  options still operate from `[WebVideoOptions]` in the configuration.
* If a converter hangs, the proxy stops it when the client disconnects.
  WebOne measures CPU use with Windows performance counters.

## Added (Go port only)

| Option | Section | What it does |
|---|---|---|
| `SslCaDirectory` | `[SecureProxy]` | sign with the CA of the D-Modem BBS ([bbs-ca.md](bbs-ca.md)) |
| `SslTrustOwnCA` | `[SecureProxy]` | also trust the CA of the proxy for the sites that the proxy gets |
| `DnsServer` | `[Server]` | find the addresses of sites through this DNS server (`host` or `host:port`) |

`RemoteHttpVersion` also accepts `3.0` (refer to [HTTP/3 and gRPC](http3-grpc.md)).

`SslProtocols` and `SslCipherSuites` operate as in WebOne. retrossl
serves SSL 2.0 and 3.0 (refer to [Old SSL clients](old-ssl.md)).

The proxy gives its version as `0.19.0-go`.
