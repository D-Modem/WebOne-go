# Use the CA of the D-Modem BBS

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
