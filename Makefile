PREFIX ?= $(HOME)/.local
UNITDIR ?= $(HOME)/.config/systemd/user

.PHONY: build test install enable uninstall

build:
	go build -o claude-inhibit .

test:
	go vet ./...
	go test ./...

install: build
	install -Dm755 claude-inhibit $(PREFIX)/bin/claude-inhibit
	install -Dm644 claude-inhibit.service $(UNITDIR)/claude-inhibit.service
	systemctl --user daemon-reload
	@systemctl --user is-active --quiet claude-inhibit && systemctl --user restart claude-inhibit || true

enable: install
	systemctl --user enable --now claude-inhibit

uninstall:
	-systemctl --user disable --now claude-inhibit
	rm -f $(PREFIX)/bin/claude-inhibit $(UNITDIR)/claude-inhibit.service
	systemctl --user daemon-reload
