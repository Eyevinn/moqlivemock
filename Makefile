.PHONY: all
all: lint test coverage check-licenses build

# Add programs to build here. Should be placed in the cmd/ directory.
# For example cmd/example/main.go. Add more programs in the build line with a space between.
.PHONY: build
build: mlmpub mlmsub

.PHONY: lint
lint: prepare
	golangci-lint run

.PHONY: prepare
prepare:
	go mod tidy

# Build binaries and write them to out/
# Same list of programs as in build.
mlmpub mlmsub:
	go build -ldflags "-X github.com/Eyevinn/moqlivemock/internal.commitVersion=$$(git describe --tags HEAD) -X github.com/Eyevinn/moqlivemock/internal.commitDate=$$(git log -1 --format=%ct)" -o out/$@ ./cmd/$@

.PHONY: test
test: prepare
	go test ./...

.PHONY: coverage
coverage:
	# Ignore (allow) packages without any tests
	set -o pipefail
	go test ./... -coverprofile coverage.out
	set +o pipefail
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func coverage.out -o coverage.txt
	tail -1 coverage.txt

.PHONY: check
check: prepare
	golangci-lint run

.PHONY: clean
clean:
	rm -f out/*
	rm -r examples-out/*

.PHONY: install
install: all
	cp out/* $(GOPATH)/bin/

.PHONY: update
update:
	go get -t -u ./...

.PHONY: check-licenses
check-licenses: prepare
	wwhrd check

.PHONY: venv
venv: .venv/bin/activate

.venv/bin/activate:
	python3 -m venv .venv
	.venv/bin/pip install --upgrade pip
	.venv/bin/pip install pre-commit==4.2.0
	touch .venv/bin/activate

.PHONY: pre-commit-install
pre-commit-install: venv
	.venv/bin/pre-commit install

.PHONY: pre-commit
pre-commit: venv
	.venv/bin/pre-commit run --all-files

.PHONY: check
check: prepare pre-commit
	golangci-lint run