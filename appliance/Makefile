# Helpers for building + smoke-testing the kvm-center appliance qcow2.
#
# `make build` runs the full virt-builder pipeline (Linux + libguestfs
# required). `make smoke` boots the freshly-built qcow2 with cloud-init
# user-data and polls /api/version. `make clean` wipes the output dir.
#
# Local devs: `make build && make smoke` on a Linux host with libvirt.
# CI: .gitea/workflows/appliance-build.yml runs the same.

OUT ?= ./out
VERSION := $(shell tr -d '[:space:]' < ../server/VERSION)
QCOW := $(OUT)/kvm-center-appliance-$(VERSION).qcow2

.PHONY: build smoke clean version

version:
	@echo "$(VERSION)"

build:
	./build.sh $(OUT)

smoke:
	./smoke.sh $(QCOW) $(VERSION)

clean:
	rm -rf $(OUT)
