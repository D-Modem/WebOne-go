# Old SSL clients

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
