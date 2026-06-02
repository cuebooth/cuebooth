.PHONY: build build-windows test vet tidy run clean

GOFLAGS    ?= -trimpath
LDFLAGS    ?= -s -w
BIN_DIR    := bin
PKG        := ./cmd/cuebooth-server
BIN_NAME   := cuebooth-server

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
