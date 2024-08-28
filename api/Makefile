IMAGE_DEST ?= docker-daemon:0g-seving-agent
VERSION	?= $(shell git describe --tags --abbrev=8)
BASE_IMAGE ?= gcr.io/distroless/static:latest
ARCH	   ?= amd64

export GO111MODULE := on
export GOSUMDB	 := off
export CGO_ENABLED := 0

lint:
	golangci-lint run --timeout 10m -v --max-same-issues 0

build: clean
	for arch in $(ARCH); do \
		mkdir -p ./build/$$arch; \
		GOOS=linux GOARCH=$$arch \
			go build -trimpath \
			-o ./build/$$arch/agent . ; \
	done

clean:
	rm -rf ./build

oci: build
	for arch in $(ARCH); do \
		skopeo --insecure-policy copy --src-tls-verify=false \
			docker://$(BASE_IMAGE) oci:build/oci:$(VERSION)-$$arch; \
		umoci insert --image build/oci:$(VERSION)-$$arch build/$$arch/agent /usr/bin/agent; \
		umoci config --image build/oci:$(VERSION)-$$arch \
			--config.entrypoint /usr/bin/agent \
			--author 0glab --created $$(date -u +%FT%T.%NZ) \
			--architecture $$arch --os linux; \
	done

release: oci
	for arch in $(ARCH); do \
		skopeo --insecure-policy copy --dest-tls-verify=false --format=v2s2 \
			oci:build/oci:$(VERSION)-$$arch $(IMAGE_DEST):$(VERSION)-$$arch; \
	done

hardhat-env:
	docker build -f ./libs/0g-serving-contract/intergrate/Dockerfile -t hardhat-node-with-contract:dev ./libs/0g-serving-contract

install-tools:
	go install -v github.com/go-swagger/go-swagger/cmd/swagger@v0.30.3
	go install -v github.com/swaggo/swag/cmd/swag@v1.8.10
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.56.2

.PHONY: lint build clean oci release hardhat-env install-tools
