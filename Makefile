NO_COLOR=\033[0m
OK_COLOR=\033[32;01m
ERROR_COLOR=\033[31;01m
WARN_COLOR=\033[33;01m
DEPS=$(shell go list -f '{{range .TestImports}}{{.}} {{end}}' ./...)
GOPKGS=$(shell go list -f '{{.ImportPath}}' ./...)
PKGSDIRS=$(shell go list -f '{{.Dir}}' ./...)
VERSION=$(shell echo `whoami`-`git rev-parse --short HEAD`-`date -u +%Y%m%d%H%M%S`)

.PHONY: all dist format lint vet build test setup tools deps updatedeps bench clean
.SILENT: all dist format lint vet build test setup tools deps updatedeps bench clean

all: clean format lint vet build test

format:
	@echo "$(OK_COLOR)==> Checking format$(ERROR_COLOR)"
	@echo $(PKGSDIRS) | xargs -I '{p}' -n1 goimports -e -l {p} | sed "s/^/Failed: /"
	@echo "$(NO_COLOR)\c"

lint:
	@echo "$(OK_COLOR)==> Linting$(ERROR_COLOR)"
	#Not linting everything as the generated code doesn't pass lint
	@echo $(GOPATH)/src/github.com/kat-co/wadl2go | xargs -I '{p}' -n1 golint {p}  | sed "s/^/Failed: /"
	@echo "$(NO_COLOR)\c"

vet:
	@echo "$(OK_COLOR)==> Vetting$(ERROR_COLOR)"
	@echo $(GOPKGS) | xargs -I '{p}' -n1 go vet {p}  | sed "s/^/Failed: /"
	@echo "$(NO_COLOR)\c"

build:
	@echo "$(OK_COLOR)==> Building$(NO_COLOR)"
	go build ./...

clean:
	@echo "$(OK_COLOR)==> Cleaning$(NO_COLOR)"
	rm -rf build
	rm -rf $(GOPATH)/pkg/*
	rm -f $(GOPATH)/bin/als

tools:
	@echo "$(OK_COLOR)==> Installing tools$(NO_COLOR)"
	#Great tools to have, and used in the build file
	go get golang.org/x/tools/cmd/goimports
	go get golang.org/x/tools/cmd/vet
	go get golang.org/x/tools/cmd/cover
	go get github.com/golang/lint/golint
	go get github.com/go-utils/ufs
	go get github.com/go-utils/unet
	go get github.com/metaleap/go-xsd/types
	go get github.com/metaleap/go-xsd-pkg/www.w3.org/2001/xml.xsd_go
	# Wanted to get this built so in build can use this to generate the wadl go xsd file
	# Will add in the future
	#cd $(GOPATH)/src/github.com/metaleap/go-xsd/types/xsd-makepkg | go build | go install

test:
	@echo "$(OK_COLOR)==> Testing$(NO_COLOR)"
	go test $(TEST_FLAGS) -short -ldflags -linkmode=external -covermode=count ./...
