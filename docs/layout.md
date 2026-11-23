# Source layout and tests

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
