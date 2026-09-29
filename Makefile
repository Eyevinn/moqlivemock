.PHONY: all build mlmpub mlmsub mlmrel build-linux test coverage check check-licenses pre-commit pre-commit-install codespell clean install update

all: check build test

# Add programs to build here. Should be placed in the cmd/ directory.
build: mlmpub mlmsub mlmrel

# Binaries are built in module mode, also inside a go.work workspace: they use
# the dependencies in go.mod, and carry the version Go embeds from the git tag
# and commit, which a workspace build does not (see internal/buildinfo.go).
mlmpub mlmsub mlmrel:
	GOWORK=off go build -o out/$@ ./cmd/$@

build-linux:
	GOOS=linux GOARCH=amd64 GOWORK=off go build -o out/mlmpub-linux ./cmd/mlmpub

test:
	go test -race ./...

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

pre-commit: venv/bin/pre-commit test
	venv/bin/pre-commit run --all-files

venv/bin/pre-commit venv/bin/codespell:
	python3 -m venv venv
	venv/bin/pip install --upgrade pip
	venv/bin/pip install pre-commit==4.2.0
	venv/bin/pip install codespell

codespell: venv/bin/codespell
	venv/bin/codespell -S venv,references,coverage.html,'*.mp4' -L ue,trun,truns

clean:
	rm -rf out/ coverage.out coverage.html coverage.txt venv/

install:
	GOWORK=off go install ./cmd/mlmpub ./cmd/mlmsub ./cmd/mlmrel

update:
	go get -t -u ./...
