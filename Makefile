BINARY := ghost
PKG := github.com/rcliao/ghost
MAIN := ./cmd/ghost

.PHONY: build test vet clean install

build:
	go build -o $(BINARY) $(MAIN)

test:
	go test ./... -v

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

install:
	go install $(MAIN)
