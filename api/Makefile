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

oci-with-config: build
	for arch in $(ARCH); do \
		skopeo --insecure-policy copy --src-tls-verify=false \
			docker://$(BASE_IMAGE) oci:build/oci:$(VERSION)-$$arch; \
		umoci insert --image build/oci:$(VERSION)-$$arch build/$$arch/broker /usr/bin/broker; \
        umoci insert --image build/oci:$(VERSION)-$$arch ./integration/akash-all-in-one/config.local.yaml /etc/config.yaml; \
		umoci config --image build/oci:$(VERSION)-$$arch \
			--config.entrypoint /usr/bin/broker \
			--author 0glab --created $$(date -u +%FT%T.%NZ) \
			--architecture $$arch --os linux; \
	done

release-with-config: oci-with-config
	for arch in $(ARCH); do \
		skopeo --insecure-policy copy --dest-tls-verify=false --format=v2s2 \
			oci:build/oci:$(VERSION)-$$arch $(IMAGE_DEST):$(VERSION)-$$arch; \
	done

release: oci
	for arch in $(ARCH); do \
		skopeo --insecure-policy copy --dest-tls-verify=false --format=v2s2 \
			oci:build/oci:$(VERSION)-$$arch $(IMAGE_DEST):$(VERSION)-$$arch; \
	done

hardhat-env:
	docker build -f ./libs/0g-serving-contract/intergrate/Dockerfile -t hardhat-node-with-contract:dev ./libs/0g-serving-contract

generate-provider-api:
	docker run --rm -v ./provider/doc:/local openapitools/openapi-generator-cli generate -i /local/swagger.yaml -g html2 -o /local/api 

generate-provider-api-md:
	docker run --rm -v ./provider/doc:/opt swagger2markup/swagger2markup convert -i /opt/swagger.yaml -f /opt/swagger
	asciidoctor-pdf ./provider/doc/swagger.adoc -o ./provider/doc/0G-Serving-Broker-API.pdf

package: generate-provider-api
	@mkdir -p /tmp/provider-broker && \
	cp ./integration/README.md /tmp/provider-broker/ && \
	rsync -av --exclude 'config.local.*' ./integration/provider/ /tmp/provider-broker && \
	cp ./provider/doc/api/index.html /tmp/provider-broker/api.html && \
	tar -czf /tmp/provider-broker.tar.gz -C /tmp provider-broker && \
	rm -rf /tmp/provider-broker && \
	echo "Package created at /tmp/provider-broker.tar.gz"

install-tools:
	go install -v github.com/go-swagger/go-swagger/cmd/swagger@v0.30.3
	go install -v github.com/swaggo/swag/cmd/swag@v1.8.10
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.56.2

.PHONY: lint build clean oci release hardhat-env install-tools
