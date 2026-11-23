# HTTP/3 and gRPC

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
