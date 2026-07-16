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
	# Unlink first: running sessions hold the old inode, so removing the
	# directory entry avoids "text file busy" while leaving them untouched.
	rm -f ~/.local/bin/$(BINARY) ~/.local/bin/$(SIDECAR)
	cp $(BINARY) $(SIDECAR) ~/.local/bin/
	ln -sf $(BINARY) ~/.local/bin/ijarvis
