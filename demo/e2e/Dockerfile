# Self-contained E2E runner. Same base as demo/walkthroughs but no
# ffmpeg — these tests assert, they don't record.
#
# Build:
#   docker build -t kvmc-e2e demo/e2e
#
# Run (against the demo stack on this host):
#   docker run --rm \
#     --add-host=host.docker.internal:host-gateway \
#     -e KVMC_DEMO_URL=https://host.docker.internal:8290 \
#     -v "$(pwd)/demo/e2e:/work" \
#     kvmc-e2e

FROM mcr.microsoft.com/playwright:v1.50.0-jammy

WORKDIR /work
COPY package.json ./
RUN npm install --silent

COPY . .

ENV KVMC_DEMO_URL=https://host.docker.internal:8290

ENTRYPOINT ["npx", "playwright", "test"]
