# Mákina Dark

Plataforma de automação para **canais "dark"** (sem apresentador) no YouTube e no TikTok. A partir de um nicho, a IA cria a identidade do canal (nome, conceito, logo, capa, abertura e encerramento) e produz vídeos completos: roteiro, narração, cenas, legendas, trilha, thumbnail e versão vertical. Depois agenda e publica tudo, manualmente ou no **Piloto Automático**.

```
Nicho ─► Identidade do canal ─► Roteiro (35–55 cenas) ─► Narração + imagens/clipes por cena
      ─► Renderização FFmpeg (16:9 + Shorts 9:16 + legendas) ─► Agendamento ─► YouTube / TikTok
```

## Componentes

| Serviço | Tecnologia | Função |
|---|---|---|
| `frontend` | Vue 3 + Vite, servido por nginx | Painel de controle; faz proxy de `/api` e `/media` para o backend |
| `backend` | Go 1.27 + Fiber + GORM, FFmpeg | API REST, pipeline de geração, renderização, agendador e Piloto Automático |
| `postgres` | PostgreSQL 16 | Banco de dados (sem `POSTGRES_HOST`, usa SQLite local) |
| `image-ranker` | Python + FastAPI + CLIP | Escolhe, entre as fotos e vídeos do Pexels, o que mais combina com cada cena |

Detalhes em [docs/arquitetura.md](docs/arquitetura.md).

## Início rápido (Docker)

Pré-requisitos: Docker com Compose v2.24 ou mais recente. Reserve **pelo menos 8 GB de RAM** para o Docker: o build da imagem `image-ranker` (PyTorch) e as renderizações FFmpeg consomem bastante memória.

```bash
# 1. Configure as chaves (o arquivo é ignorado pelo git)
cp backend/.env.example backend/.env
#    edite backend/.env: JWT_SECRET, ADMIN_PASSWORD e pelo menos uma chave de LLM

# 2. Suba tudo
docker compose up -d --build

# 3. Acesse
#    http://localhost:3000
```

No primeiro start, com o banco vazio, o backend cria a conta `ADMIN_EMAIL` com a senha `ADMIN_PASSWORD`. Se `ADMIN_PASSWORD` estiver vazio, uma senha aleatória é gerada e impressa no log:

```bash
docker logs makinadark-backend 2>&1 | grep "Admin user seeded"
```

Troque a senha em **Configurações → Alterar Senha**.

### Chaves mínimas para funcionar

| Para… | Precisa de |
|---|---|
| Gerar roteiros e textos | Pelo menos uma: `GROQ_API_KEY`, `GEMINI_API_KEY`, `ANTHROPIC_API_KEY` ou `OPENAI_API_KEY` |
| Logo, capa e thumbnail com boa qualidade | `OPENAI_API_KEY` (gpt-image); o Gemini é o reserva |
| Narração de qualidade | `ELEVENLABS_API_KEY`; sem ela, cai no OpenAI TTS e depois numa voz gratuita limitada |
| Imagens e clipes reais nas cenas | `PEXELS_API_KEY` |
| Publicar | App OAuth do YouTube e/ou do TikTok, com a conta conectada em cada canal |

Lista completa de variáveis: [docs/configuracao.md](docs/configuracao.md).

## Desenvolvimento local

```bash
# Backend (usa SQLite e ./media quando POSTGRES_HOST/R2 estão vazios)
cd backend
go run ./cmd/api              # porta 8080
go test ./...

# Frontend (proxy de /api e /media para BACKEND_URL)
cd frontend
npm ci
BACKEND_URL=http://localhost:8080 npm run dev   # http://localhost:5173
```

Sem `BACKEND_URL`, o Vite usa `http://localhost:8081`, o backend publicado pelo `docker compose`. FFmpeg e ffprobe precisam estar no `PATH` para renderizar fora do Docker.

## Documentação

- [Arquitetura e fluxos](docs/arquitetura.md): pipeline de vídeo, status, Piloto Automático, armazenamento, rotinas em segundo plano
- [Configuração](docs/configuracao.md): todas as variáveis de ambiente e a ordem de uso de cada provedor
- [Deploy e operação](docs/deploy.md): Docker, EasyPanel, OAuth do YouTube/TikTok, backup e atualização
- [API REST](docs/api.md): referência dos endpoints
- [Solução de problemas](docs/solucao-de-problemas.md)

## Estrutura do repositório

```
backend/
  cmd/api/              ponto de entrada (main.go)
  internal/auth/        JWT, hash de senha, state OAuth assinado
  internal/config/      leitura das variáveis de ambiente
  internal/db/          conexão (Postgres/SQLite), migrações, conta inicial
  internal/handlers/    rotas e handlers HTTP
  internal/models/      modelos GORM
  internal/services/    IA, TTS, mídia, renderização, agendador, Piloto Automático, uploads
frontend/
  src/App.vue           aplicação (componente único)
  nginx.conf.template   proxy /api e /media + cache de estáticos
image-ranker/           microserviço CLIP
docker-compose.yml
```
