
.PHONY: all
all: lint-go test-unit build

.PHONY: build
build:
	../dev/tools/build-go-binaries --working-dir ./

.PHONY: test-unit
test-unit:
	../dev/tools/test-unit --working-dir ./cmd/
	../dev/tools/test-unit --working-dir ./pkg/

.PHONY: lint-go
lint-go:
	../dev/tools/lint-go --working-dir ./

.PHONY: clean
clean:
	rm -rf bin
