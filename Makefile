BINARY := peercash-bridge
PKG := ./cmd/peercash-bridge
LDFLAGS := -s -w

.PHONY: all build windows linux test vet clean

all: test windows linux

build:
	go build -o bin/$(BINARY) $(PKG)

windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-windows-amd64.exe $(PKG)

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 $(PKG)

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin
