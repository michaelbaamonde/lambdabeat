.PHONY: lambdabeat
lambdabeat:
	go build

.PHONY: clean
clean:
	go clean
	rm -f lambdabeat lambdabeat-*.tar.gz

.PHONY: update-deps
update-deps:
	glide update --no-recursive --strip-vcs

# This is called by the beats packer before building starts
.PHONY: before-build
before-build:

VERSION=$(shell grep 'var Version' main.go | sed 's/.*"\([^"]*\)"/\1/')
TARBALL=lambdabeat-$(VERSION)-x86_64.tar.gz
.PHONY: release
release: clean lambdabeat
	tar -c --transform 's,^,lambdabeat-$(VERSION)/,' \
      -zf $(TARBALL) \
	  lambdabeat lambdabeat.template.json lambdabeat.yml README.org
	tar tf $(TARBALL)
