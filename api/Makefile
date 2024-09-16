IMAGE_DEST ?= docker-daemon:ghcr.io/0glabs/0g-serving-broker
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
			-o ./build/$$arch/broker . ; \
	done

clean:
	rm -rf ./build

oci: build
	for arch in $(ARCH); do \
		skopeo --insecure-policy copy --src-tls-verify=false \
			docker://$(BASE_IMAGE) oci:build/oci:$(VERSION)-$$arch; \
		umoci insert --image build/oci:$(VERSION)-$$arch build/$$arch/broker /usr/bin/broker; \
		umoci config --image build/oci:$(VERSION)-$$arch \
			--config.entrypoint /usr/bin/broker \
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

generate-user-api:
	docker run --rm -v ./user/doc:/local openapitools/openapi-generator-cli generate -i /local/swagger.yaml -g html2 -o /local/api 

generate-provider-api:
	docker run --rm -v ./provider/doc:/local openapitools/openapi-generator-cli generate -i /local/swagger.yaml -g html2 -o /local/api 

package: generate-user-api generate-provider-api
	@mkdir -p /tmp/0g-serving && \
	cp ./integration/README.md /tmp/0g-serving/ && \
	cp ./integration/introduction.md /tmp/0g-serving/ && \
	rsync -av --exclude 'config.local.*' ./integration/provider/ /tmp/0g-serving/provider/ && \
	rsync -av --exclude 'config.local.*' ./integration/user/ /tmp/0g-serving/user/ && \
	cp ./user/doc/api/index.html /tmp/0g-serving/user/api.html && \
	cp ./provider/doc/api/index.html /tmp/0g-serving/provider/api.html && \
	tar -czf /tmp/0g-serving.tar.gz -C /tmp 0g-serving && \
	rm -rf /tmp/0g-serving && \
	echo "Package created at /tmp/0g-serving.tar.gz"

install-tools:
	go install -v github.com/go-swagger/go-swagger/cmd/swagger@v0.30.3
	go install -v github.com/swaggo/swag/cmd/swag@v1.8.10
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.56.2

.PHONY: lint build clean oci release hardhat-env install-tools
