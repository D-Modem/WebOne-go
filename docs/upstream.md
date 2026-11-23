# Where this comes from

Alexander Tauenis and the WebOne Development Team wrote WebOne in C#,
under the BSD-style licence in [LICENSE.txt](../LICENSE.txt). This repository
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
configuration. That documentation also applies to this port. [Differences from WebOne](differences.md)
gives the differences.
