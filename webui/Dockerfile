FROM nginx:alpine

RUN apk --no-cache add bash

# Copy static assets
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY docker-entrypoint.sh /docker-entrypoint-custom.sh
RUN chmod +x /docker-entrypoint-custom.sh

# The HTML lives in server/static so it stays in sync with the embedded version.
COPY webui/index.html /usr/share/nginx/html/index.html

EXPOSE 80

ENV KVM_CENTER_SERVER_HOST=kvm-center-server
ENV KVM_CENTER_SERVER_PORT=8090

ENTRYPOINT ["/docker-entrypoint-custom.sh"]
CMD ["nginx", "-g", "daemon off;"]
