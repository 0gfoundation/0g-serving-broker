IMAGE_DEST ?= docker-daemon:ghcr.io/0glabs/0g-serving-broker
VERSION	?= $(shell git describe --tags --abbrev=8)
BASE_IMAGE ?= gcr.io/distroless/static:latest
ARCH	   ?= amd64

export GO111MODULE := on
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
	docker build -f ./libs/0g-serving-contract/integrate/Dockerfile -t hardhat-node-with-contract:dev ./libs/0g-serving-contract

generate-inference-api:
	docker run --rm -v ./inference/doc:/local openapitools/openapi-generator-cli generate -i /local/swagger.yaml -g html2 -o /local/api 

generate-inference-api-md:
	docker run --rm -v ./inference/doc:/opt swagger2markup/swagger2markup convert -i /opt/swagger.yaml -f /opt/swagger
	asciidoctor-pdf ./inference/doc/swagger.adoc -o ./inference/doc/0G-Serving-Inference-Broker-API.pdf

generate-inference-router-api:
	docker run --rm -v ./inference-router/doc:/local openapitools/openapi-generator-cli generate -i /local/swagger.yaml -g html2 -o /local/api 

package-inference: generate-inference-api
	@mkdir -p /tmp/inference-broker && \
	cp ./inference/integration/README.md /tmp/inference-broker/ && \
	rsync -av --exclude '**.local.*' ./inference/integration/provider/ /tmp/inference-broker && \
	cp ./inference/doc/api/index.html /tmp/inference-broker/api.html && \
	tar -czf /tmp/inference-broker.tar.gz -C /tmp inference-broker && \
	rm -rf /tmp/inference-broker && \
	echo "Package created at /tmp/inference-broker.tar.gz"

package-inference-router-all-in-one: generate-inference-router-api
	@mkdir -p /tmp/inference-router-all-ine-one && \
	rsync -av --exclude '**.local.*' --exclude '*.key' --exclude '*.pem'  ./inference-router/integration/prod/all-in-one/ /tmp/inference-router-all-ine-one && \
	cp ./inference-router/doc/api/index.html /tmp/inference-router-all-ine-one/api.html && \
	tar -czf /tmp/inference-router-all-ine-one.tar.gz -C /tmp inference-router-all-ine-one && \
	rm -rf /tmp/inference-router-all-ine-one && \
	echo "Package created at /tmp/inference-router-all-ine-one.tar.gz"
	
package-fine-tuning:
	@mkdir -p /tmp/fine-tuning-broker && \
	cp ./fine-tuning/integration/README.md /tmp/fine-tuning-broker/ && \
	rsync -av --exclude '**.local.*' ./fine-tuning/integration/provider/ /tmp/fine-tuning-broker && \
	cp ./fine-tuning/doc/api/index.html /tmp/fine-tuning-broker/api.html && \
	tar -czf /tmp/fine-tuning-broker.tar.gz -C /tmp fine-tuning-broker && \
	rm -rf /tmp/fine-tuning-broker && \
	echo "Package created at /tmp/fine-tuning-broker.tar.gz"

install-tools:
	go install -v github.com/go-swagger/go-swagger/cmd/swagger@v0.30.3
	go install -v github.com/swaggo/swag/cmd/swag@v1.8.10
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.56.2

.PHONY: lint build clean oci release hardhat-env install-tools
