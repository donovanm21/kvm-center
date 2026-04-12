FROM nginx:alpine

RUN apk --no-cache add bash

# Copy static assets (paths relative to repo root build context)
COPY webui/nginx.conf.template /etc/nginx/templates/default.conf.template
COPY webui/docker-entrypoint.sh /docker-entrypoint-custom.sh
RUN chmod +x /docker-entrypoint-custom.sh

# The HTML + PWA assets live in webui/ (synced from server/static/).
COPY webui/index.html /usr/share/nginx/html/index.html
COPY webui/manifest.json /usr/share/nginx/html/manifest.json
COPY webui/sw.js /usr/share/nginx/html/sw.js
COPY webui/icon-192.svg /usr/share/nginx/html/icon-192.svg
COPY webui/icon-512.svg /usr/share/nginx/html/icon-512.svg

EXPOSE 80

ENV KVM_CENTER_SERVER_HOST=kvm-center-server
ENV KVM_CENTER_SERVER_PORT=8090

ENTRYPOINT ["/docker-entrypoint-custom.sh"]
CMD ["nginx", "-g", "daemon off;"]
