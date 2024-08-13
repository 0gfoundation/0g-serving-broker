lint:
	golangci-lint run --timeout 10m -v --max-same-issues 0

install-tools:
	go install -v github.com/go-swagger/go-swagger/cmd/swagger@v0.30.3
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.56.2

.PHONY: lint install-tools
