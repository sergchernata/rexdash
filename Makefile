BIN ?= $(HOME)/.local/bin

.PHONY: build test test-short install

# Two binaries: rexdash (git, prs, ci, eas, builds, session) and rexdash-aws (AWS panes,
# which rexdash hands off to), so only AWS panes load the AWS SDK.
build:
	go build -trimpath -ldflags="-s -w" -o bin/ ./cmd/...

test:
	go test ./...

# Skips the end-to-end tests that build the binaries and run them in a pty.
test-short:
	go test -short ./...

# Write beside, then rename over: running panes see a complete new file and restart into it.
install: build
	install -d $(BIN)
	for f in rexdash-aws rexdash; do cp bin/$$f $(BIN)/$$f.new && mv $(BIN)/$$f.new $(BIN)/$$f; done
