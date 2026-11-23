# Build and run

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
