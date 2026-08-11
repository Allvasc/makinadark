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
# "<project>_<service>" naming.
ENV BACKEND_HOST=backend
ENV BACKEND_PORT=8080

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
