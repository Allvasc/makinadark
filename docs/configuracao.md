# Configuração

O backend lê variáveis de ambiente. No `docker compose`, elas vêm de dois arquivos, ambos opcionais e ignorados pelo git:

- `backend/.env`: desenvolvimento local (modelo em [`backend/.env.example`](../backend/.env.example))
- `.env` na raiz, ao lado do `docker-compose.yml`: é o arquivo que o **EasyPanel** gera a partir da aba *Environment*

Depois de alterar um `.env`, recrie o backend para carregar os novos valores:

```bash
docker compose up -d backend
```

Algumas chaves também podem ser definidas pela tela **Configurações** do painel (OpenAI, Gemini, ElevenLabs e os apps OAuth do YouTube e do TikTok). Elas ficam no banco, valem na hora sem reiniciar e **têm prioridade sobre o `.env`**. Campos deixados em branco na tela mantêm o valor atual.

> Um valor vazio ou que comece com `your_` (os exemplos do `.env.example`) conta como **não configurado**.

## Essenciais

| Variável | Padrão | Descrição |
|---|---|---|
| `JWT_SECRET` | aleatório a cada start | Assina as sessões e o `state` do OAuth. **Defina**, senão todo restart desloga todo mundo. Gere com `openssl rand -hex 32` |
| `ADMIN_EMAIL` | `admin@makinadark.com` | Conta criada só quando o banco não tem nenhum usuário |
| `ADMIN_PASSWORD` | aleatória (impressa no log) | Senha dessa conta inicial |
| `ALLOW_REGISTRATION` | `false` | `true` libera o botão "Criar Conta" e o `POST /api/auth/register` |

## Provedores de IA

| Variável | Uso |
|---|---|
| `GROQ_API_KEY` | 1º LLM tentado para texto (gratuito) |
| `GEMINI_API_KEY` | 2º LLM; imagens de marca (reserva da OpenAI) e imagens de cena (reserva do Pollinations) |
| `ANTHROPIC_API_KEY` | 3º LLM (Claude Sonnet 5.5) |
| `OPENAI_API_KEY` | Gerador principal de **logo, capa e thumbnail**; 4º LLM; TTS de reserva |
| `OPENAI_IMAGE_QUALITY` | `low`, `medium` ou `high`. Sem ela: `high` para logo e capa, `medium` para thumbnail |
| `OPENAI_IMAGE_MODEL` | Padrão `gpt-image-1.5` |
| `ELEVENLABS_API_KEY` | Narração principal |
| `GEMINI_FREE_TIER_DAILY_LIMIT` | Padrão `20`. Cota diária gratuita da chave Gemini, só para exibição no painel de custos |
| `POLLINATIONS_API_KEY` | Lida, mas **não usada**: o Pollinations é chamado de forma anônima, que é mais rápido |

É preciso pelo menos uma chave de LLM. A ordem de uso de cada provedor está em [arquitetura.md](arquitetura.md#provedores-e-ordem-de-uso).

## Mídia e renderização

| Variável | Padrão | Descrição |
|---|---|---|
| `PEXELS_API_KEY` | – | Fotos e clipes reais para as cenas (gratuito) |
| `SHOTSTACK_API_KEY` | – | Renderização em nuvem, usada **só pelo Piloto Automático** quando definida |
| `SHORTS_MAX_SECONDS` | `60` | Duração máxima da versão 9:16 enviada ao TikTok. `0` = vídeo inteiro |
| `IMAGE_RANKER_URL` | definida pelo compose | URL do microserviço CLIP. Sem ela, o primeiro resultado do Pexels é usado |

## Armazenamento

| Variável | Descrição |
|---|---|
| `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_BUCKET` | Cloudflare R2. Com as quatro definidas, todo arquivo gerado vai para o bucket |
| `R2_PUBLIC_BASE_URL` | URL pública do bucket (domínio `r2.dev` ou customizado). É usada para servir e para apagar arquivos |

Sem R2, os arquivos ficam em `./media` (no Docker, o volume `backend_media`) e são servidos em `/media`.

## Banco de dados

| Variável | Padrão | Descrição |
|---|---|---|
| `POSTGRES_HOST` | vazio = SQLite | No compose, sempre `postgres` |
| `POSTGRES_PORT` | `5432` | |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` | `makina` / `makinadark` / `makinadark` | No compose, os mesmos valores configuram o container do Postgres. **Troque a senha em produção** (antes do primeiro start; depois, troque também dentro do banco) |
| `DB_LOG_LEVEL` | `warn` | `info` registra cada query SQL; também aceita `error` e `silent` |

Sem `POSTGRES_HOST`, o banco é um arquivo SQLite em `./data/makinadark.db`.

## Publicação (OAuth)

| Variável | Descrição |
|---|---|
| `YOUTUBE_CLIENT_ID`, `YOUTUBE_CLIENT_SECRET` | App OAuth do Google com a YouTube Data API v3 |
| `TIKTOK_CLIENT_ID`, `TIKTOK_CLIENT_SECRET` | *Client key* e *secret* do TikTok for Developers (Content Posting API) |
| `REDIRECT_URI` | URL de retorno, a mesma para os dois provedores. Com o frontend do projeto: `<URL do painel>/api/oauth/callback`, por exemplo `http://localhost:3000/api/oauth/callback` ou `https://seu-dominio/api/oauth/callback`. Precisa estar cadastrada **exatamente igual** nos consoles do Google e do TikTok |

Passo a passo em [deploy.md](deploy.md#configurando-o-oauth).

## Servidor e rede

| Variável | Padrão | Onde | Descrição |
|---|---|---|---|
| `PORT` | `8080` | backend | Porta HTTP (o compose fixa 8080) |
| `TZ` | `America/Sao_Paulo` | compose | Fuso usado para a "hora preferida" do Piloto Automático e para a virada de dia da cota do Gemini |
| `CORS_ORIGINS` | portas locais do Vite e do frontend | backend | Só importa se o frontend **não** passar pelo nginx do projeto |
| `FRONTEND_PORT` | `3000` | compose | Porta publicada do painel |
| `BACKEND_HOST`, `BACKEND_PORT` | `backend`, `8080` | imagem do frontend | Para onde o nginx encaminha `/api` e `/media`. Mude quando o backend tiver outro hostname interno (ex.: EasyPanel) |
| `BACKEND_URL` | `http://localhost:8081` | `npm run dev` | Destino do proxy do Vite em desenvolvimento |

## Variáveis obsoletas

`USER_PHONE_NUMBER` aparece em `.env` antigos, mas não é lida pelo código e pode ser removida.
