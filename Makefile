# Copyright 2016 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# The binaries to build (just the basenames).
BINS := ddbbolt

# Where to push the docker image.
REGISTRY ?= r-push.lerch.org

# This version-strategy uses git tags to set the version string
VERSION ?= $(shell git describe --tags --always --dirty)
#
# This version-strategy uses a manual value to set the version string
#VERSION ?= 1.2.3

# Podman rootless needs 777. Otherwise should be 755
BINDIRMODE ?= 777
###
### These variables should not need tweaking.
###

DKR := $(shell if command -v docker > /dev/null 2>&1; then echo "docker"; else echo "podman"; fi)

# Rootless podman, "root" in the shell will be the uid of the user
UID ?= $(shell if [ "$(DKR)" = "podman" ]; then echo 0; else id -u; fi)
GID ?= $(shell if [ "$(DKR)" = "podman" ]; then echo 0; else id -g; fi)

SRC_DIRS := cmd pkg # directories which hold app source (not vendored)

# Windows not working atm
#ALL_PLATFORMS := linux/amd64 linux/arm linux/arm64 linux/ppc64le linux/s390x
# Unlikely I'll run on ppc or s390x anytime soon
ALL_PLATFORMS := linux/amd64 linux/arm linux/arm64

# Used internally.  Users should pass GOOS and/or GOARCH.
# hacked this to at least guess if go isn't installed on the host
OS := $(if $(GOOS),$(GOOS),$(shell go env GOOS 2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]'))
ARCH := $(if $(GOARCH),$(GOARCH),$(shell go env GOARCH 2>/dev/null || echo 'amd64'))

BASEIMAGE ?= scratch # gcr.io/distroless/static

TAG := $(VERSION)__$(OS)_$(ARCH)

BUILD_IMAGE ?= golang:1.15.5-alpine

BIN_EXTENSION :=
ifeq ($(OS), windows)
  BIN_EXTENSION := .exe
endif

# If you want to build all binaries, see the 'all-build' rule.
# If you want to build all containers, see the 'all-container' rule.
# If you want to build AND push all containers, see the 'all-push' rule.
all: # @HELP builds binaries for one platform ($OS/$ARCH)
all: build

# For the following OS/ARCH expansions, we transform OS/ARCH into OS_ARCH
# because make pattern rules don't match with embedded '/' characters.

build-%:
	@$(MAKE) build                        \
	    --no-print-directory              \
	    GOOS=$(firstword $(subst _, ,$*)) \
	    GOARCH=$(lastword $(subst _, ,$*))

container-%:
	@$(MAKE) container                    \
	    --no-print-directory              \
	    GOOS=$(firstword $(subst _, ,$*)) \
	    GOARCH=$(lastword $(subst _, ,$*))

push-%:
	@$(MAKE) push                         \
	    --no-print-directory              \
	    GOOS=$(firstword $(subst _, ,$*)) \
	    GOARCH=$(lastword $(subst _, ,$*))

all-build: # @HELP builds binaries for all platforms
all-build: $(addprefix build-, $(subst /,_, $(ALL_PLATFORMS)))

all-container: # @HELP builds containers for all platforms
all-container: $(addprefix container-, $(subst /,_, $(ALL_PLATFORMS)))

all-push: # @HELP pushes containers for all platforms to the defined registry
all-push: $(addprefix push-, $(subst /,_, $(ALL_PLATFORMS)))

# The following structure defeats Go's (intentional) behavior to always touch
# result files, even if they have not changed.  This will still run `go` but
# will not trigger further work if nothing has actually changed.
OUTBINS = $(foreach bin,$(BINS),bin/$(OS)_$(ARCH)/$(bin)$(BIN_EXTENSION))

build: $(OUTBINS)

# Directories that we need created to build/test.
BUILD_DIRS := bin/$(OS)_$(ARCH)     \
              .go/bin/$(OS)_$(ARCH) \
              .go/cache

# Each outbin target is just a facade for the respective stampfile target.
# This `eval` establishes the dependencies for each.
$(foreach outbin,$(OUTBINS),$(eval  \
    $(outbin): .go/$(outbin).stamp  \
))
# This is the target definition for all outbins.
$(OUTBINS):
	@true

# Each stampfile target can reference an $(OUTBIN) variable.
$(foreach outbin,$(OUTBINS),$(eval $(strip   \
    .go/$(outbin).stamp: OUTBIN = $(outbin)  \
)))
# This is the target definition for all stampfiles.
# This will build the binary under ./.go and update the real binary iff needed.
STAMPS = $(foreach outbin,$(OUTBINS),.go/$(outbin).stamp)
.PHONY: $(STAMPS)
$(STAMPS): go-build
	@echo "binary: $(OUTBIN)"
	@if ! cmp -s .go/$(OUTBIN) $(OUTBIN); then  \
	    mv -f .go/$(OUTBIN) $(OUTBIN);          \
	    date >$@;                               \
	fi

# This runs the actual `go build` which updates all binaries.
go-build: $(BUILD_DIRS)
	@echo
	@echo "building for $(OS)/$(ARCH)"
	@mkdir -p "$$(pwd)/.go/bin/$(OS)_$(ARCH)"
	@chmod $(BINDIRMODE) "$$(pwd)/.go/bin/$(OS)_$(ARCH)"
	@$(DKR) run                                                 \
	    --rm                                                    \
	    -u $(UID):$(GID)                                        \
	    -v $$(pwd):/src                                         \
	    -w /src                                                 \
	    -v $$(pwd)/.go/bin/$(OS)_$(ARCH):/go/bin                \
	    -v $$(pwd)/.go/bin/$(OS)_$(ARCH):/go/bin/$(OS)_$(ARCH)  \
	    -v $$(pwd)/.go/cache:/.cache                            \
	    --env HOME=/                                            \
	    --env HTTP_PROXY=$(HTTP_PROXY)                          \
	    --env HTTPS_PROXY=$(HTTPS_PROXY)                        \
	    $(BUILD_IMAGE)                                          \
	    /bin/sh -c "                                            \
	        ARCH=$(ARCH)                                        \
	        OS=$(OS)                                            \
	        VERSION=$(VERSION)                                  \
	        ./build/build.sh                                    \
	    "

# Example: make shell CMD="-c 'date > datefile'"
shell: # @HELP launches a shell in the containerized build environment
shell: $(BUILD_DIRS)
	@echo "launching a shell in the containerized build environment"
	@$(DKR) run                                                 \
	    -ti                                                     \
	    --rm                                                    \
	    -u $(UID):$(GID)                                        \
	    -v $$(pwd):/src                                         \
	    -w /src                                                 \
	    -v $$(pwd)/.go/bin/$(OS)_$(ARCH):/go/bin                \
	    -v $$(pwd)/.go/bin/$(OS)_$(ARCH):/go/bin/$(OS)_$(ARCH)  \
	    -v $$(pwd)/.go/cache:/.cache                            \
	    --env HOME=/                                            \
	    --env HTTP_PROXY=$(HTTP_PROXY)                          \
	    --env HTTPS_PROXY=$(HTTPS_PROXY)                        \
	    $(BUILD_IMAGE)                                          \
	    /bin/sh $(CMD)

CONTAINER_DOTFILES = $(foreach bin,$(BINS),.container-$(subst /,_,$(REGISTRY)/$(bin))-$(TAG))

container containers: # @HELP builds containers for one platform ($OS/$ARCH)
container containers: $(CONTAINER_DOTFILES)
	@for bin in $(BINS); do              \
	    echo "container: $(REGISTRY)/$$bin:$(TAG)"; \
	done

# Each container-dotfile target can reference a $(BIN) variable.
# This is done in 2 steps to enable target-specific variables.
$(foreach bin,$(BINS),$(eval $(strip                                 \
    .container-$(subst /,_,$(REGISTRY)/$(bin))-$(TAG): BIN = $(bin)  \
)))
$(foreach bin,$(BINS),$(eval                                                                   \
    .container-$(subst /,_,$(REGISTRY)/$(bin))-$(TAG): bin/$(OS)_$(ARCH)/$(bin) Dockerfile.in  \
))
# This is the target definition for all container-dotfiles.
# These are used to track build state in hidden files.
$(CONTAINER_DOTFILES):
	@sed                                 \
	    -e 's|{ARG_BIN}|$(BIN)|g'        \
	    -e 's|{ARG_ARCH}|$(ARCH)|g'      \
	    -e 's|{ARG_OS}|$(OS)|g'          \
	    -e 's|{ARG_FROM}|$(BASEIMAGE)|g' \
	    Dockerfile.in > .dockerfile-$(BIN)-$(OS)_$(ARCH)
	@$(DKR) build -t $(REGISTRY)/$(BIN):$(TAG) -f .dockerfile-$(BIN)-$(OS)_$(ARCH) .
	@$(DKR) images -q $(REGISTRY)/$(BIN):$(TAG) > $@
	@echo

push: # @HELP pushes the container for one platform ($OS/$ARCH) to the defined registry
push: $(CONTAINER_DOTFILES)
	@for bin in $(BINS); do                    \
	    $(DKR) push $(REGISTRY)/$$bin:$(TAG);  \
	done

# TODO: podman and docker are pretty different wrt manifests and workflow here
#       docker is experimental CLI and requires pushed images (which then should probably
#       be untagged on the server), podman can push everything at once when the manifest
#       is cleaned
manifest-list: # @HELP builds a manifest list of containers for all platforms
manifest-list: all-container
	@export DOCKER_CLI_EXPERIMENTAL=enabled  &&                                          \
	if $(DKR) --version | grep -q podman; then                                           \
		for bin in $(BINS); do                                                             \
			$(DKR) manifest create $(REGISTRY)/$$bin:$(VERSION);                             \
			for platform in $(ALL_PLATFORMS); do                                             \
				$(DKR) manifest add --arch $$(echo $$platform | cut -d/ -f2)                   \
					$(REGISTRY)/$$bin:$(VERSION)                                                 \
					$(REGISTRY)/$$bin:$(VERSION)__$$(echo $$platform | sed 's#/#_#g');           \
			done;                                                                            \
			$(DKR) manifest push --all $(REGISTRY)/$$bin:$(VERSION)                          \
											docker://$(REGISTRY)/$$bin:$(VERSION);                           \
		done;                                                                              \
	else                                                                                 \
		for bin in $(BINS); do                                                             \
			cmd="$(DKR) manifest create $(REGISTRY)/$$bin:$(VERSION)";                       \
			for platform in $(ALL_PLATFORMS); do                                             \
			  cmd="$$cmd $(REGISTRY)/$$bin:$(VERSION)__$$(echo $$platform | sed 's#/#_#g')"; \
				$(DKR) push $(REGISTRY)/$$bin:$(VERSION)__$$(echo $$platform | sed 's#/#_#g'); \
			done;                                                                            \
			eval "$$cmd";                                                                    \
			for platform in $(ALL_PLATFORMS); do                                             \
				$(DKR) manifest annotate --arch $$(echo $$platform | cut -d/ -f2)              \
					$(REGISTRY)/$$bin:$(VERSION)                                                 \
					$(REGISTRY)/$$bin:$(VERSION)__$$(echo $$platform | sed 's#/#_#g');           \
			done;                                                                            \
			$(DKR) manifest push $(REGISTRY)/$$bin:$(VERSION);                               \
		done;                                                                              \
	fi

version: # @HELP outputs the version string
version:
	@echo $(VERSION)

test: # @HELP runs tests, as defined in ./build/test.sh
test: $(BUILD_DIRS)
	@$(DKR) run                                                 \
	    -i                                                      \
	    --rm                                                    \
	    -u $(UID):$(GID)                                        \
	    -v $$(pwd):/src                                         \
	    -w /src                                                 \
	    -v $$(pwd)/.go/bin/$(OS)_$(ARCH):/go/bin                \
	    -v $$(pwd)/.go/bin/$(OS)_$(ARCH):/go/bin/$(OS)_$(ARCH)  \
	    -v $$(pwd)/.go/cache:/.cache                            \
	    --env HTTP_PROXY=$(HTTP_PROXY)                          \
	    --env HTTPS_PROXY=$(HTTPS_PROXY)                        \
	    $(BUILD_IMAGE)                                          \
	    /bin/sh -c "                                            \
	        ARCH=$(ARCH)                                        \
	        OS=$(OS)                                            \
	        VERSION=$(VERSION)                                  \
	        ./build/test.sh $(SRC_DIRS)                         \
	    "

$(BUILD_DIRS):
	@mkdir -p $@

clean: # @HELP removes built binaries and temporary files
clean: container-clean bin-clean

container-clean:
	@rm -rf .container-* .dockerfile-*;                                             \
	for bin in $(BINS); do                                                          \
		if $(DKR) --version |grep -q podman; then                                     \
			$(DKR) image exists "$(REGISTRY)/$$bin:$(VERSION)" &&                       \
			$(DKR) image rm "$(REGISTRY)/$$bin:$(VERSION)";                             \
		else                                                                          \
			$(DKR) image rm "$(REGISTRY)/$$bin:$(VERSION)";                             \
		fi;                                                                           \
		for platform in $(ALL_PLATFORMS); do                                          \
			if $(DKR) --version |grep -q podman; then                                   \
				$(DKR) image exists                                                       \
				  "$(REGISTRY)/$$bin:$(VERSION)__$$(echo $$platform | sed 's#/#_#g')" &&  \
				$(DKR) image rm                                                           \
				  "$(REGISTRY)/$$bin:$(VERSION)__$$(echo $$platform | sed 's#/#_#g')";    \
			else                                                                        \
				$(DKR) image rm                                                           \
				  "$(REGISTRY)/$$bin:$(VERSION)__$$(echo $$platform | sed 's#/#_#g')";    \
			fi;                                                                         \
		done;                                                                         \
	done; true

bin-clean:
	rm -rf .go bin

help: # @HELP prints this message
help:
	@echo "VARIABLES:"
	@echo "  BINS = $(BINS)"
	@echo "  OS = $(OS)"
	@echo "  ARCH = $(ARCH)"
	@echo "  REGISTRY = $(REGISTRY)"
	@echo
	@echo "TARGETS:"
	@grep -E '^.*: *# *@HELP' $(MAKEFILE_LIST)    \
	    | awk '                                   \
	        BEGIN {FS = ": *# *@HELP"};           \
	        { printf "  %-30s %s\n", $$1, $$2 };  \
	    '
