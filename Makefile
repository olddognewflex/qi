.PHONY: test run tidy

tidy:
	go mod tidy

test:
	go test ./...

run:
	go run ./cmd/qi
