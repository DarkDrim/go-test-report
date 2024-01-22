VERSION := $(shell grep -o 'const.*=*' version.go  | cut -d '"' -f2)
MACOS := go-test-report-darwin-v$(VERSION)
LINUX := go-test-report-linux-v$(VERSION)
WINDOWS := go-test-report-windows-v$(VERSION)

LIN_DIR := release_builds/linux-amd64/
MAC_DIR := release_builds/darwin-amd64/
WIN_DIR := release_builds/windows-amd64/

GO_VERSION = 1.21.5

.PHONY: help
help: ## List of available commands
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: go-version
go-version: ## Print Golang version
	@echo "$(GO_VERSION)"

genbuild:
	go build

buildall: genbuild
	echo "Building..."

	mkdir -p $(LIN_DIR)
	mkdir -p $(MAC_DIR)
	mkdir -p $(WIN_DIR)

	go mod verify

	echo "Linux 64bit"
	GOOS=linux GOARCH=amd64 go build -o release_builds/linux-amd64/
	(cd $(LIN_DIR); shasum -a 256 go-test-report |  cut -d ' ' -f 1 > $(LINUX).sha256)
	(cd $(LIN_DIR); tar -czf $(LINUX).tgz go-test-report $(LINUX).sha256)

	echo "Darwin (MacOS) 64bit"
	GOOS=darwin GOARCH=amd64 go build -o release_builds/darwin-amd64/
	(cd $(MAC_DIR); shasum -a 256 go-test-report |  cut -d ' ' -f 1 > $(MACOS).sha256)
	(cd $(MAC_DIR); tar -czf $(MACOS).tgz go-test-report $(MACOS).sha256)

	echo "Windows 64bit"
	GOOS=windows GOARCH=amd64 go build -o release_builds/windows-amd64/
	(cd $(WIN_DIR); shasum -a 256 go-test-report.exe |  cut -d ' ' -f 1 > $(WINDOWS).sha256)
	(cd $(WIN_DIR); zip -r $(WINDOWS).zip go-test-report.exe $(WINDOWS).sha256)

	echo "...Done!"

dockertest: genbuild
	docker build --build-arg GO_VERSION=$(GO_VERSION) . -t go-test-report-test-runner:$(VERSION)

.PHONY: lint
lint: ## Run linter (https://github.com/golangci/golangci-lint/releases)
	docker run --rm -v $(PWD):/app -w /app golangci/golangci-lint:v1.55.2 golangci-lint run -v --timeout 3m
