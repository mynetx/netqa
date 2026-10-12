# netqa developer tasks. `make deploy` is the one to remember: a merge does not
# update the running daemon until the binary is rebuilt and the agent restarted.

LABEL  := com.mynetx.netqa
BIN    := $(HOME)/.local/bin/netqa
PORT   ?= 8799
LOGDIR := $(HOME)/Library/Logs/netqa

.PHONY: check test vet fmt build deploy install status logs backup

check: fmt vet test

test:
	go test ./...

vet:
	go vet ./...

# Fails if any file needs gofmt (CI uses the same check).
fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

build:
	go build -o $(BIN) ./cmd/netqa

# Rebuild, restart the launchd agent in place, and wait until it answers.
deploy: check build
	launchctl kickstart -k gui/$$(id -u)/$(LABEL)
	@for i in $$(seq 1 15); do \
	  curl -fsS http://127.0.0.1:$(PORT)/healthz >/dev/null 2>&1 && { echo "netqa up on :$(PORT)"; exit 0; }; \
	  sleep 1; done; echo "netqa did not come up — see $(LOGDIR)"; exit 1

# First-time install (writes the launchd agent).
install:
	./install.sh

status:
	@launchctl list | grep $(LABEL) || echo "$(LABEL) not loaded"
	@curl -fsS http://127.0.0.1:$(PORT)/api/status && echo

logs:
	tail -n 50 -f $(LOGDIR)/netqa.err.log

# Timestamped, consistent DB snapshot (default dir: <data dir>/backups).
backup:
	$(BIN) backup $(DIR)
