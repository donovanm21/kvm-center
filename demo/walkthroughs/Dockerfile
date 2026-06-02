# Self-contained walkthrough recorder. Bundles Playwright's chromium
# image with ffmpeg so the host doesn't need node/npm/ffmpeg.
#
# Build:
#   docker build -t kvmc-walkthroughs demo/walkthroughs
#
# Run (against the demo stack on this host):
#   docker run --rm --network host \
#     -e KVMC_DEMO_URL=https://localhost:8290 \
#     -v "$(pwd)/server/docs/media:/out" \
#     -v "$(pwd)/demo/walkthroughs:/work" \
#     kvmc-walkthroughs
#
# The entrypoint runs every spec, converts each captured .webm into a
# .webp, and drops the result in /out (mounted at server/docs/media).

FROM mcr.microsoft.com/playwright:v1.50.0-jammy

RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /work
COPY package.json ./
RUN npm install --silent

COPY . .

ENV KVMC_DEMO_URL=https://host.docker.internal:8290
ENV OUT_DIR=/out

ENTRYPOINT ["/work/run.sh"]
