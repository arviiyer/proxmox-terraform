#!/bin/sh
set -eu

template_dir="/app/sandbox-infra-template"
runtime_dir="/sandbox-infra"

mkdir -p "$runtime_dir"

# Keep the mounted runtime config in sync with the image so live deploys don't
# keep using stale Terraform files from a previous rollout.
for file in .gitignore main.tf outputs.tf variables.tf versions.tf; do
    if [ -f "$template_dir/$file" ]; then
        cp "$template_dir/$file" "$runtime_dir/$file"
    fi
done

# This app is the only writer of the mounted Terraform state. If the container
# restarted, any previous terraform process is gone, so a leftover local lock
# file is stale and safe to remove.
rm -f "$runtime_dir/.terraform.tfstate.lock.info"

exec /app/sandbox
