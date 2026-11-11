PREFIX ?= /usr/local
SYSCONFDIR ?= /etc
GO ?= go

# The program, html/ and logo.webp go together in $(LIBDIR), since
# ContentDirectory=/html is looked for next to the program; bin/webone
# links to it.
LIBDIR ?= $(PREFIX)/lib/webone
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

all: webone

webone:
	$(GO) build -trimpath -o webone ./cmd/webone

test:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt: the files above need formatting"; exit 1; }
	$(GO) vet ./...
	$(GO) test -race ./...

# Installs the program, its pages, the configuration (keeping any already
# there), the systemd unit and the logrotate rule.  DESTDIR stages it.
install: webone
	install -D -m 0755 webone $(DESTDIR)$(LIBDIR)/webone
	mkdir -p $(DESTDIR)$(LIBDIR)/html $(DESTDIR)$(PREFIX)/bin
	cp -R html/. $(DESTDIR)$(LIBDIR)/html/
	install -m 0644 logo.webp LICENSE.txt $(DESTDIR)$(LIBDIR)/
	ln -sf $(LIBDIR)/webone $(DESTDIR)$(PREFIX)/bin/webone
	@if [ ! -e $(DESTDIR)$(SYSCONFDIR)/webone.conf ]; then \
		install -D -m 0644 webone.conf $(DESTDIR)$(SYSCONFDIR)/webone.conf; \
	else echo "kept $(SYSCONFDIR)/webone.conf"; fi
	@for f in codepage.conf escargot.conf; do \
		if [ ! -e $(DESTDIR)$(SYSCONFDIR)/webone.conf.d/$$f ]; then \
			install -D -m 0644 $$f $(DESTDIR)$(SYSCONFDIR)/webone.conf.d/$$f; \
		else echo "kept $(SYSCONFDIR)/webone.conf.d/$$f"; fi; \
	done
	sed 's|/usr/local/bin/webone|$(PREFIX)/bin/webone|' webone.service > webone.service.out
	install -D -m 0644 webone.service.out $(DESTDIR)$(SYSCONFDIR)/systemd/system/webone.service
	rm -f webone.service.out
	install -D -m 0644 webone.logrotate $(DESTDIR)$(SYSCONFDIR)/logrotate.d/webone

# Removes what install put in place, except the configuration.
uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/webone
	rm -f $(DESTDIR)$(LIBDIR)/webone $(DESTDIR)$(LIBDIR)/logo.webp $(DESTDIR)$(LIBDIR)/LICENSE.txt
	rm -rf $(DESTDIR)$(LIBDIR)/html
	-rmdir $(DESTDIR)$(LIBDIR)
	rm -f $(DESTDIR)$(SYSCONFDIR)/systemd/system/webone.service $(DESTDIR)$(SYSCONFDIR)/logrotate.d/webone

# A tarball to copy to another machine, such as: make dist GOOS=linux GOARCH=386
dist: webone
	rm -rf dist/webone
	mkdir -p dist/webone
	cp -R webone html logo.webp LICENSE.txt README.md webone.conf codepage.conf escargot.conf \
		webone.service webone.logrotate dist/webone/
	tar -C dist -czf dist/webone-$(VERSION)-$$($(GO) env GOOS)-$$($(GO) env GOARCH).tar.gz webone
	rm -rf dist/webone

clean:
	rm -f webone
	rm -rf dist

.PHONY: all webone test install uninstall dist clean
