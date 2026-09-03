.PHONY: build build-windows web web-clean test vet tidy run clean

GOFLAGS    ?= -trimpath
LDFLAGS    ?= -s -w
BIN_DIR    := bin
PKG        := ./cmd/cuebooth-server
BIN_NAME   := cuebooth-server

# Build the Flutter web client into the directory the server embeds. Run this
# before build/build-windows if the binary should carry a client; without it the
# server still runs and serves a page saying so.
#
# --no-web-resources-cdn makes the loader read CanvasKit from this bundle.
# Without it the page fetches the engine from gstatic.com and the copy staged
# here is embedded but never asked for.
WEB_DIST := internal/webui/dist
FLUTTER   ?= flutter

web:
	cd ../client && $(FLUTTER) build web --release --no-web-resources-cdn
	@find $(WEB_DIST) -mindepth 1 ! -name .gitkeep -delete
	@cp -R ../client/build/web/. $(WEB_DIST)/
	@echo "web client staged in $(WEB_DIST)"

web-clean:
	@find $(WEB_DIST) -mindepth 1 ! -name .gitkeep -delete
	@echo "cleared $(WEB_DIST)"

# Native build (matches the host OS).
build:
	@mkdir -p $(BIN_DIR)
	go build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN_DIR)/$(BIN_NAME) $(PKG)

# Cross-compile for Windows — the production target.
build-windows:
	@mkdir -p $(BIN_DIR)
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags='$(LDFLAGS)' \
		-o $(BIN_DIR)/$(BIN_NAME).exe $(PKG)

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# Run locally against the example config (good for development).
run:
	go run $(PKG) -config configs/cuebooth.example.toml

clean:
	rm -rf $(BIN_DIR)
