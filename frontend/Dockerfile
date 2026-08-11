# Stage 1: Build Vue 3 Frontend
FROM node:20-alpine AS builder

WORKDIR /app

COPY package*.json ./
RUN npm install

COPY . .
RUN npm run build

# Stage 2: Serve with Nginx
FROM nginx:alpine

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf.template /etc/nginx/templates/default.conf.template

# Defaults match this repo's own docker-compose.yml service name/port -
# override BACKEND_HOST (and BACKEND_PORT if needed) at deploy time when the
# backend is reachable under a different internal hostname, e.g. EasyPanel's
# "<project>_<service>" naming. Declared as ARG too (promoted to ENV below)
# because some PaaS build pipelines (EasyPanel included) inject configured
# service variables as --build-arg rather than container runtime env - ARG
# alone wouldn't survive into the running container, and ENV alone would
# silently ignore a --build-arg with no matching ARG declaration.
ARG BACKEND_HOST=backend
ARG BACKEND_PORT=8080
ENV BACKEND_HOST=${BACKEND_HOST}
ENV BACKEND_PORT=${BACKEND_PORT}

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
