BINARY_NAME=cheburnetd
CMD_PATH=./cmd/cheburnetd

.PHONY: all build-arm64 build-mips clean

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -trimpath -o bin/$(BINARY_NAME)-arm64 $(CMD_PATH)

build-mips:
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -ldflags="-s -w" -trimpath -o bin/$(BINARY_NAME)-mipsle $(CMD_PATH)

build-x86:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o bin/$(BINARY_NAME)-amd64 $(CMD_PATH)

clean:
	rm -rf bin/