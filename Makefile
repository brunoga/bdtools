PKG     := ./...
COVER   := coverage.out
VERSION := $(shell git describe --tags --dirty --always 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test test-purego lint vet cover clean docker-build

build:
	go build $(LDFLAGS) -o mvcdec ./cmd/mvcdec
	go build $(LDFLAGS) -o mvctools ./cmd/mvctools

test:
	go test -race $(PKG)

# The pure-Go decoder, which every non-amd64 platform runs.
test-purego:
	go test -tags purego $(PKG)

vet:
	go vet $(PKG)

lint:
	golangci-lint run $(PKG)

cover:
	go test -race -coverprofile=$(COVER) $(PKG)
	go tool cover -html=$(COVER) -o coverage.html

clean:
	rm -f mvcdec mvctools $(COVER) coverage.html

docker-build:
	docker build -f Dockerfile.mvctools --build-arg VERSION=$(VERSION) -t mvctools .
