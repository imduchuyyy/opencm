.PHONY: build dev clean

build:
	CGO_ENABLED=1 go build -o opencm .

dev:
	CGO_ENABLED=1 go run .

clean:
	rm -f opencm
