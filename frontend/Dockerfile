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

# Vite inlines `import.meta.env.VITE_*` at BUILD time, so these must be present
# during `npm run build` — not just at container runtime. The two base URLs
# default to the nginx-proxied `/api/v1`; VITE_DEFAULT_TENANT_ID has no default
# (it's environment-specific — the login page hard-errors without it), so the
# caller must pass it as a build arg (docker-compose supplies the fixed dev
# bootstrap tenant id).
ARG VITE_API_BASE_URL=/api/v1
ARG VITE_AUTH_BASE_URL=/api/v1
ARG VITE_DEFAULT_TENANT_ID

# Vite build emits to /app/dist.
RUN VITE_API_BASE_URL="$VITE_API_BASE_URL" \
    VITE_AUTH_BASE_URL="$VITE_AUTH_BASE_URL" \
    VITE_DEFAULT_TENANT_ID="$VITE_DEFAULT_TENANT_ID" \
    npm run build

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
