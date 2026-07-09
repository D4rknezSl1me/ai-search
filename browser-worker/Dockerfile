# Official Playwright image ships Node + Chromium + all OS deps at a matching
# version, so no `playwright install` dance and no missing shared libraries.
# Keep this tag in lockstep with the "playwright" version in package.json.
FROM mcr.microsoft.com/playwright:v1.49.1-noble

WORKDIR /app

# Install deps first for layer caching. The base image already bundles the
# browser binaries, so npm only needs the Playwright JS package + tsc.
COPY package.json package-lock.json* ./
RUN npm install --no-audit --no-fund

# Build TypeScript → dist/.
COPY tsconfig.json ./
COPY src ./src
RUN npm run build

ENV NODE_ENV=production
# pwuser ships with the base image and is non-root.
USER pwuser

CMD ["node", "dist/index.js"]
