BIN     := pmtui
PREFIX  ?= $(HOME)/.local/bin

.PHONY: build install run clean

build:
	go build -o $(BIN) .

install: build
	install -d $(PREFIX)
	install -m 0755 $(BIN) $(PREFIX)/$(BIN)
	rm -f $(BIN)

run:
	go run .

clean:
	rm -f $(BIN)
