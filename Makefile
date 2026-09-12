.PHONY: build run test vet clean

build:
	go build -o auteur ./cmd/auteur

run: build
	./auteur

test:
	go test ./...

vet:
	go vet ./...
	gofmt -l .

clean:
	rm -f auteur
