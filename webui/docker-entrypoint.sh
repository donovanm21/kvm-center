#!/bin/sh
# Substitute env vars into the nginx template and start nginx.
set -e

envsubst '${KVM_CENTER_SERVER_HOST} ${KVM_CENTER_SERVER_PORT}' \
  < /etc/nginx/templates/default.conf.template \
  > /etc/nginx/conf.d/default.conf

echo "nginx: proxying /api/ → http://${KVM_CENTER_SERVER_HOST}:${KVM_CENTER_SERVER_PORT}"

exec "$@"
