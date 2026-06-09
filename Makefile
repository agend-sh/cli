BINARY := agend
MODULE := github.com/agend-sh/cli
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install clean test release proto

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/agend

install: build
	cp bin/$(BINARY) /usr/local/bin/$(BINARY)

test:
	go test ./...

proto:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/agentd/v1/agent.proto

clean:
	rm -rf bin/

release:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 ./cmd/agend
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 ./cmd/agend
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-amd64 ./cmd/agend
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-arm64 ./cmd/agend
