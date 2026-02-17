.PHONY: all build test coverage check check-licenses pre-commit pre-commit-install codespell clean install update

LDFLAGS = -X github.com/Eyevinn/moqlivemock/internal.commitVersion=$$(git describe --tags HEAD 2>/dev/null || echo dev-$$(git rev-parse --short HEAD)) \
          -X github.com/Eyevinn/moqlivemock/internal.commitDate=$$(git log -1 --format=%ct)

all: check build test

# Add programs to build here. Should be placed in the cmd/ directory.
build: mlmpub mlmsub

mlmpub mlmsub:
	go build -ldflags "$(LDFLAGS)" -o out/$@ ./cmd/$@

test:
	go test ./...

coverage:
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out -o coverage.txt
	@echo "Coverage report: coverage.html"

check:
	golangci-lint run

check-licenses:
	wwhrd check

pre-commit-install: venv/bin/pre-commit
	venv/bin/pre-commit install

pre-commit: venv/bin/pre-commit
	venv/bin/pre-commit run --all-files

venv/bin/pre-commit venv/bin/codespell:
	python3 -m venv venv
	venv/bin/pip install pre-commit codespell

codespell: venv/bin/codespell
	venv/bin/codespell -S venv,references,coverage.html,'*.mp4' -L ue,trun,truns

clean:
	rm -rf out/ coverage.out coverage.html coverage.txt venv/

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/mlmpub
	go install -ldflags "$(LDFLAGS)" ./cmd/mlmsub

update:
	go get -t -u ./...
