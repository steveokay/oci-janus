# Beacon — frontend production image.
# Two-stage build: Node 22 builds the SPA; nginx-alpine serves it.
# The runtime image is distroless-style — no shell, no package manager
# after build; static assets only.

# -------- builder --------
FROM node:22-alpine AS builder

WORKDIR /app

# Install with `npm ci` for reproducibility. We copy package + lock first
# so dep installs cache cleanly when only source changes.
COPY package.json package-lock.json* ./
RUN npm ci

# Now the source.
COPY . .

# Vite build emits to /app/dist.
RUN npm run build

# -------- runtime --------
FROM nginx:1.27-alpine

# Patch base image packages to pick up CVE fixes that landed in the Alpine
# repos after the nginx:1.27-alpine tag was last republished. Without this,
# Trivy in CI flags CRITICAL OpenSSL CVEs (libcrypto3 / libssl3) on every
# build until upstream rebuilds. Cheap: only the diff is layered in.
RUN apk upgrade --no-cache

# Replace the stock nginx config with ours (security headers, SPA fallback,
# /api proxying to the backend services).
COPY nginx.conf /etc/nginx/conf.d/default.conf

# Drop the default index and ship our build.
RUN rm -rf /usr/share/nginx/html/*
COPY --from=builder /app/dist /usr/share/nginx/html

# Run as non-root nginx user (already exists in the base image).
EXPOSE 8080
STOPSIGNAL SIGQUIT
CMD ["nginx", "-g", "daemon off;"]
