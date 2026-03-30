BINARY     := jarvis
SIDECAR    := jarvis-sidecar
LDFLAGS    := -s -w
GOFLAGS    := CGO_ENABLED=0

.PHONY: build test clean install

build:
	$(GOFLAGS) go build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/jarvis/
	$(GOFLAGS) go build -ldflags="$(LDFLAGS)" -o $(SIDECAR) ./cmd/sidecar/

test:
	go test ./...

clean:
	rm -f $(BINARY) $(SIDECAR)

install: build
	cp $(BINARY) $(SIDECAR) ~/.local/bin/
