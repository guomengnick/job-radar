.PHONY: test vet build lambda plan apply clean extension-config

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

# Pre-fills the 送到雷達 extension's settings, so nobody copies values out of
# the AWS console. The file holds the clip secret: it is git-ignored, and the
# secret goes from AWS straight into the file without being printed.
extension-config:
	@url=$$(terraform -chdir=infra output -raw clip_url) && \
	secret=$$(aws ssm get-parameter --region ap-southeast-2 --name /job-radar/clip-secret --with-decryption --query Parameter.Value --output text) && \
	umask 077 && printf 'self.RADAR_DEFAULTS = { endpoint: "%s", secret: "%s" };\n' "$$url" "$$secret" > extension/config.local.js && \
	echo "wrote extension/config.local.js; reload the extension in chrome://extensions"
