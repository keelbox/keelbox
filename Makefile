.PHONY: build check test snapshot test-install e2e e2e-hetzner e2e-aws e2e-digitalocean e2e-gcp

build:
	scripts/build.sh

check:
	scripts/check.sh

test:
	mise exec -- go test ./...

snapshot:
	mise exec -- goreleaser release --snapshot --clean --skip=sign,sbom,publish

test-install: snapshot
	scripts/test-install.sh

e2e: snapshot
	scripts/e2e.sh lxd

e2e-hetzner:
	scripts/e2e.sh hetzner

e2e-aws:
	scripts/e2e.sh aws

e2e-digitalocean:
	scripts/e2e.sh digitalocean

e2e-gcp:
	scripts/e2e.sh gcp
