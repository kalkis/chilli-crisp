BINARY := chilli-crisp
DIST   := dist
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build test vet release clean

build:
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

release: test vet
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		out=$(DIST)/$(BINARY)-$$os-$$arch$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o $$out . || exit 1; \
	done

clean:
	rm -rf $(BINARY) $(DIST)
