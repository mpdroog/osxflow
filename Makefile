# Every directory under cmd/ is a tool; nothing here needs editing to add
# one. The directory name is the binary name.
#
# Keep tool names to 15 characters or fewer. Linux truncates a process's
# comm to 15 bytes, so a longer name shows up in ps cut short and `pgrep -x`
# cannot match it at all -- only pgrep -f against the full path works.

TOOLS   := $(notdir $(wildcard cmd/*))
BINDIR  := bin
PREFIX  ?= $(HOME)/.local

# CGO_ENABLED=0 is the point of this repository, not an optimisation: it is
# what makes every binary a single file with no distro dependencies.
export CGO_ENABLED = 0

.PHONY: all $(TOOLS) test lint fuzz icons install clean

all: $(TOOLS)

$(TOOLS):
	@mkdir -p $(BINDIR)
	go build -o $(BINDIR)/$@ ./cmd/$@
	@echo "built $(BINDIR)/$@"

test:
	go test ./... -cover

# Short by default so it can run on every change; the corpus in
# testdata/fuzz keeps whatever earlier runs found.
fuzz:
	go test ./internal/calc   -run=XXX -fuzz=FuzzEval             -fuzztime=20s
	go test ./internal/search -run=XXX -fuzz=FuzzFilter           -fuzztime=20s
	go test ./internal/ui     -run=XXX -fuzz=FuzzModelTyping      -fuzztime=20s
	go test ./internal/dock   -run=XXX -fuzz=FuzzLayout           -fuzztime=20s
	go test ./internal/paint  -run=XXX -fuzz=FuzzRoundRect        -fuzztime=20s
	go test ./internal/text   -run=XXX -fuzz=FuzzTruncate         -fuzztime=20s
	go test ./internal/geom   -run=XXX -fuzz=FuzzI16              -fuzztime=10s
	go test ./internal/geom   -run=XXX -fuzz=FuzzU16              -fuzztime=10s
	go test ./internal/stack  -run=XXX -fuzz=FuzzParseTrashInfo   -fuzztime=20s
	go test ./internal/stack  -run=XXX -fuzz=FuzzParseUserDirs    -fuzztime=20s

lint:
	gofmt -l .
	go vet ./...
	golangci-lint run ./...

# Rasterise the desktop's icon theme into the PNGs cmd/dock embeds.
#
# This is not part of `all`, and deliberately so: it reads the icon theme
# from disk and rewrites files that are committed, so it runs when the
# theme changes or an application is installed -- not on every build. The
# tool it needs (gdk-pixbuf-thumbnailer, which is librsvg) never ships.
icons:
	go run ./tools/mkicons

install: all
	@mkdir -p $(PREFIX)/bin
	@for t in $(TOOLS); do \
		install -m755 $(BINDIR)/$$t $(PREFIX)/bin/$$t; \
		echo "installed $(PREFIX)/bin/$$t"; \
	done

clean:
	rm -rf $(BINDIR)
