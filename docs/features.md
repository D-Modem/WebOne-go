# Features

These features operate:

* All three request modes: as a proxy, as a web server for its own pages,
  and the `http://proxy/http://site/` alternate mode. CERN-style FTP is
  also supported.
* HTTP/1.0 and HTTP/1.1 clients, with keep-alive. The reply uses chunked
  transfer only for HTTP/1.1 clients.
* Requests to sites through HTTPS, HTTP/2 and HTTP/3 (refer to [HTTP/3 and gRPC](http3-grpc.md)), with HTTP→HTTPS upgrades,
  `[ForceHttps]`, and the processing of redirect loops ("carousel").
* Sets of edits (`[Edit:...]`): all the conditions and actions of WebOne,
  with .NET regular expressions (through
  [regexp2](https://github.com/dlclark/regexp2)).
* Code pages: `OutputEncoding`, `[Translit]`, and `codepage.conf`. The
  port includes Windows, DOS, Mac, ISO and KOI8 code pages, UTF-16 and
  the CJK code pages.
* The HTTPS proxy (CONNECT): man-in-the-middle with a certificate for
  each site, for clients from SSL 2.0 to TLS 1.3 (refer to [Old SSL clients](old-ssl.md)). Also pass-through (`[ConnectPassThrough]`), non-HTTP tunnels
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
