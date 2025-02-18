GOK_PARENT_DIR := gokrazy
GOK_INSTANCE := jitaku
GOK := go run github.com/gokrazy/tools/cmd/gok@main -i $(GOK_INSTANCE) --parent_dir $(GOK_PARENT_DIR)
GOK_INSTANCE_DIR := $(GOK_PARENT_DIR)/$(GOK_INSTANCE)
GOK_CONFIG_FILE := $(GOK_INSTANCE_DIR)/config.json
GOK_BASE_CONFIG_FILE := $(GOK_INSTANCE_DIR)/config.base.json

ARTIFACT_DIR := out
ENVS_DIR := .envs

BUILD_CONFIGS_DIR := build-configs
BUILD_CONFIGS := $(basename $(notdir $(shell find $(BUILD_CONFIGS_DIR) -type f -name '*.jq')))
IMAGE_TARGETS := $(addsuffix .img,$(addprefix $(ARTIFACT_DIR)/,$(BUILD_CONFIGS)))

DEV_TARGET ?= rpi-64
WATCH_ENVS := GOKRAZY_HOSTNAME GOKRAZY_PASSWORD

.PHONY: _updateenvs get update run overwrite clean

.NOTPARALLEL: $(IMAGE_TARGETS)

all: $(IMAGE_TARGETS)

_updateenvs:
	for env in $(WATCH_ENVS); do f=$(ENVS_DIR)/$$env; v=$$(eval echo \$$$$env); if [[ "$$v" != "$$(cat $$f 2> /dev/null)" ]]; then mkdir -p $(ENVS_DIR); echo $$v > $$f; fi; done

$(GOK_CONFIG_FILE): $(BUILD_CONFIGS_DIR)/$(DEV_TARGET).jq _updateenvs $(addprefix $(ENVS_DIR)/,$(WATCH_ENVS))
	jq -f $< $(GOK_BASE_CONFIG_FILE) \
	| jq 'if env.GOKRAZY_HOSTNAME then .Update = {"Hostname":env.GOKRAZY_HOSTNAME} else . end' \
	| jq 'if env.GOKRAZY_PASSWORD then (.Update.HTTPPassword = env.GOKRAZY_PASSWORD | del(.Update.NoPassword)) else . end' \
	> $(GOK_CONFIG_FILE)

get: $(GOK_CONFIG_FILE)
	$(GOK) get --update_all

update: $(GOK_CONFIG_FILE)
	$(GOK) update

run: $(GOK_CONFIG_FILE)
	$(GOK) run

overwrite: $(GOK_CONFIG_FILE)
	$(GOK) overwrite --full $(OVERWRITE_DEVICE)

clean:
	rm -rf $(ARTIFACT_DIR) $(ENVS_DIR) $(GOK_CONFIG_FILE)

$(ARTIFACT_DIR)/%.img: $(BUILD_CONFIGS_DIR)/%.jq $(GOK_BASE_CONFIG_FILE) $(shell find . -type f -name '*.go') $(shell find $(GOK_INSTANCE_DIR) -type f -name '*.mod') $(shell find $(GOK_INSTANCE_DIR) -type f -name '*.sum')
	jq -f $< $(GOK_BASE_CONFIG_FILE) > $(GOK_CONFIG_FILE)
	mkdir -p $(ARTIFACT_DIR)
	$(GOK) overwrite --root $@
	rm $(GOK_CONFIG_FILE)
