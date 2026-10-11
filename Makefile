.PHONY: test vet build lambda plan apply clean

test:
	go test -race ./...

vet:
	go vet ./...

build:
	go build -o build/radar ./cmd/radar

# Linux arm64 binary named "bootstrap", as the provided.al2023 runtime expects.
lambda:
	mkdir -p build
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o build/bootstrap ./cmd/lambda
	cd build && rm -f lambda.zip && zip -q lambda.zip bootstrap

plan: lambda
	cd infra && terraform plan

apply: lambda
	cd infra && terraform apply

clean:
	rm -rf build
