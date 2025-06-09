IMAGE_DEST ?= docker-daemon:ghcr.io/0glabs/0g-serving-broker
VERSION	?= $(shell git describe --tags --abbrev=8)
BASE_IMAGE ?= 0g-serving-broker:latest
ARCH	   ?= amd64

export GO111MODULE := on
export CGO_ENABLED := 0

lint:
	golangci-lint run --timeout 10m -v --max-same-issues 0

build: clean
	for arch in $(ARCH); do \
		mkdir -p ./build/$$arch; \
		GOOS=linux GOARCH=$$arch \
			go build -trimpath -buildvcs=false \
			-o ./build/$$arch/broker . ; \
	done

clean:
	rm -rf ./build

submodules:
	git submodule update --init --recursive	

base-image:
	docker build -t 0g-serving-broker .

oci: build base-image submodules
	for arch in $(ARCH); do \
		skopeo --insecure-policy copy --src-tls-verify=false \
			docker-daemon:$(BASE_IMAGE) oci:build/oci:$(VERSION)-$$arch; \
		umoci insert --image build/oci:$(VERSION)-$$arch build/$$arch/broker /usr/bin/broker; \
		umoci insert --image build/oci:$(VERSION)-$$arch token-counter/ /token-counter; \
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

generate-tapp-client:
	cd api/common/tee/tapp/proto && protoc --go_out=proto --go-grpc_out=proto --go_opt=paths=source_relative --go-grpc_opt=paths=source_relative proto/tapp.proto  

hardhat-env:
	docker build -f ./libs/0g-serving-contract/integrate/Dockerfile -t raven20241/hardhat-compute-network-contract:dev ./libs/0g-serving-contract

generate-inference-api:
	docker run --rm -v ./inference/doc:/local openapitools/openapi-generator-cli generate -i /local/swagger.yaml -g html2 -o /local/api 

generate-inference-api-md:
	docker run --rm -v ./inference/doc:/opt swagger2markup/swagger2markup convert -i /opt/swagger.yaml -f /opt/swagger
	asciidoctor-pdf ./inference/doc/swagger.adoc -o ./inference/doc/0G-Serving-Inference-Broker-API.pdf

generate-fine-tuning-api:
	docker run --rm -v ./fine-tuning/doc:/local openapitools/openapi-generator-cli generate -i /local/swagger.yaml -g html2 -o /local/api 

generate-fine-tuning-api-md:
	docker run --rm -v ./fine-tuning/doc:/opt swagger2markup/swagger2markup convert -i /opt/swagger.yaml -f /opt/swagger
	asciidoctor-pdf ./fine-tuning/doc/swagger.adoc -o ./fine-tuning/doc/0G-Serving-Fine-Tuning-Broker-API.pdf

generate-inference-router-api:
	docker run --rm -v ./inference-router/doc:/local openapitools/openapi-generator-cli generate -i /local/swagger.yaml -g html2 -o /local/api 

package-inference: generate-inference-api
	@mkdir -p /tmp/inference-broker && \
	cp ./inference/integration/README.md /tmp/inference-broker/ && \
	rsync -av --exclude '**.local.*' ./inference/integration/provider/ /tmp/inference-broker && \
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
	mkdir /tmp/fine-tuning-broker/transformer && \
	mkdir /tmp/fine-tuning-broker/models && \
	rsync -av --exclude '**.local.*' ./fine-tuning/integration/prod/ /tmp/fine-tuning-broker && \
	rsync -av ./fine-tuning/execution/transformer/ /tmp/fine-tuning-broker/transformer && \
	tar -czf /tmp/fine-tuning-broker.tar.gz -C /tmp fine-tuning-broker && \
	rm -rf /tmp/fine-tuning-broker && \
	echo "Package created at /tmp/fine-tuning-broker.tar.gz"

install-tools:
	go install -v github.com/go-swagger/go-swagger/cmd/swagger@v0.30.3
	go install -v github.com/swaggo/swag/cmd/swag@v1.8.10
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.56.2
	go install github.com/ethereum/go-ethereum/cmd/abigen@v1.14.5
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@1.5.1
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@1.36.6

.PHONY: lint build clean oci release hardhat-env install-tools
