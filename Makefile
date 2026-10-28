.PHONY: test vet build lambda plan apply check-main deployed eval backfill-jd labels profile telegram-webhook dlq redrive clean extension-config

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

# What runs in AWS should be what main says. apply refuses unless this
# checkout is on main, equal to GitHub's main, with no uncommitted change in
# the code that goes into the zip. ALLOW_DIRTY=1 skips the check for an
# emergency; the fix still goes to main right after.
apply: check-main lambda
	cd infra && terraform apply

CODE := cmd internal config go.mod go.sum

check-main:
ifndef ALLOW_DIRTY
	@git fetch -q origin main
	@branch=$$(git branch --show-current); test "$$branch" = main || \
		{ echo "✋ 不在 main（現在是 $$branch）。先 git switch main"; exit 1; }
	@test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || \
		{ echo "✋ 跟 GitHub 的 main 不一樣。先 git pull --ff-only（或把本機的 commit 走 PR）"; exit 1; }
	@test -z "$$(git status --porcelain -- $(CODE))" || \
		{ echo "✋ 有沒 commit 的程式碼，會被編進去："; git status --short -- $(CODE); exit 1; }
	@echo "✓ main $$(git rev-parse --short HEAD)，跟 GitHub 一致，程式碼沒有未 commit 的修改"
endif

# Which commit each function runs, read from the deployed binaries.
deployed:
	@./scripts/deployed.sh

# Judge your labelled jobs again and compare with your labels:
#   make eval LABELS=path/labels.csv [PROFILE=path/draft-profile.md]
# One model call per job (about US$0.03). Keep both files outside the repo.
eval:
	go run ./cmd/radar eval --labels "$(LABELS)" $(if $(PROFILE),--profile "$(PROFILE)")

# A labels file to fill in for make eval: make labels > ../履歷相關/labels.csv
labels:
	@go run ./cmd/radar labels

# Point judgements made before pages were kept at their page (once).
backfill-jd:
	go run ./cmd/radar backfill-jd

# What is in the dead-letter queue, and sending it again (after a yes).
dlq:
	@go run ./cmd/radar dlq

redrive:
	@go run ./cmd/radar redrive

# Send the label buttons pressed in Telegram to the radar (once).
telegram-webhook:
	@./scripts/telegram-webhook.sh

# Replace the scoring profile in SSM with a local file, after a diff and a
# yes: make profile PROFILE=path/profile.md. No deploy needed.
profile:
	@./scripts/profile.sh "$(PROFILE)"

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
