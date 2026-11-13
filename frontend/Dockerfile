# Stage 1: Build the Vue 3 frontend. Node 24 is the active LTS (Node 20 -
# used before - reached end-of-life in April 2026).
FROM node:24-alpine AS builder

WORKDIR /app

COPY package*.json ./
# ci (not install): reproducible, installs exactly what package-lock.json pins.
RUN npm ci --no-audit --no-fund

COPY . .
RUN npm run build

# Stage 2: Serve with Nginx (stable branch, pinned)
FROM nginx:1.30-alpine

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf.template /etc/nginx/templates/default.conf.template

# Defaults match this repo's own docker-compose.yml service name/port -
# override BACKEND_HOST (and BACKEND_PORT if needed) at deploy time when the
# backend is reachable under a different internal hostname, e.g. EasyPanel's
# "<project>_<service>" naming. Declared as ARG too (promoted to ENV below)
# because some PaaS build pipelines (EasyPanel included) inject configured
# service variables as --build-arg rather than container runtime env.
ARG BACKEND_HOST=backend
ARG BACKEND_PORT=8080
ENV BACKEND_HOST=${BACKEND_HOST}
ENV BACKEND_PORT=${BACKEND_PORT}
# Only substitute our own variables - nginx's $host, $uri etc. must survive.
ENV NGINX_ENVSUBST_FILTER="^BACKEND_"

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
