# Mákina Dark — Frontend

Painel em Vue 3 + Vite (componente único em `src/App.vue`). Em produção, a imagem Docker serve o build com nginx e encaminha `/api` e `/media` ao backend (veja `nginx.conf.template`).

```bash
npm ci
BACKEND_URL=http://localhost:8080 npm run dev   # http://localhost:5173
npm run build                                   # gera dist/
```

Sem `BACKEND_URL`, o proxy do Vite usa `http://localhost:8081`, o backend publicado pelo `docker compose`.

Documentação completa do projeto: [../README.md](../README.md) e [../docs](../docs).
