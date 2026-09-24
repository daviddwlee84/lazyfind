.PHONY: build test race vet check fmt
build:
	go build -o bin/lazyfind .
test:
	go test ./...
race:
	go test -race ./...
vet:
	go vet ./...
check: test vet
fmt:
	gofmt -w main.go internal
