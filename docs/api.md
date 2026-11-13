# API REST

Base: `/api` (pelo painel, `http://localhost:3000/api`). Todas as respostas são JSON. Em caso de erro, a resposta é `{"error": "mensagem"}` com o status HTTP correspondente.

**Autenticação:** envie `Authorization: Bearer <token>` em todos os endpoints, exceto os marcados como *público*. O token vale 7 dias. Um `401` significa token ausente, inválido ou expirado. Recursos de outro usuário respondem `404`.

**Conflitos (`409`):** são devolvidos quando a ação não combina com o estado atual. Exemplos: renderizar um vídeo que já está renderizando, excluir algo em processamento, gerar uma abertura que já está sendo gerada.

## Autenticação

| Método | Rota | Descrição |
|---|---|---|
| GET | `/auth/config` | *Público.* `{"registration_enabled": bool}` |
| POST | `/auth/login` | *Público.* `{"email","password"}` → `{"token","user":{"id","email","name"}}` |
| POST | `/auth/register` | *Público.* `{"email","password","name"}` → `201` com o mesmo formato do login. `403` se `ALLOW_REGISTRATION=false` |
| PUT | `/auth/password` | `{"current_password","new_password"}` (mínimo 8 caracteres) |

## Canais

| Método | Rota | Descrição |
|---|---|---|
| GET | `/channels` | Lista os canais do usuário |
| POST | `/channels` | `{"niche", "name"?}`. Gera a identidade com IA, o logo e a capa (pode levar minutos). Responde `201 {"channel": {...}, "warning"?: "..."}`; `warning` aparece quando o logo e/ou a capa falharam |
| GET | `/channels/:id` | Detalhe do canal |
| DELETE | `/channels/:id` | Exclui o canal com todos os vídeos e arquivos. `409` se houver geração ou publicação em andamento |
| POST | `/channels/:id/generate-logo` | Gera um novo logo |
| POST | `/channels/:id/generate-banner` | Gera uma nova capa |
| POST | `/channels/:id/generate-branding` | Gera logo e capa juntos |
| POST | `/channels/:id/generate-intro` | `{"script"?}` (vazio = a IA escreve). Gera em segundo plano; acompanhe `intro_status`/`intro_progress` |
| POST | `/channels/:id/generate-outro` | Idem, para o encerramento (`outro_*`) |
| DELETE | `/channels/:id/intro` · `/outro` | Remove a abertura ou o encerramento |
| PUT | `/channels/:id/narrator-voice` | `{"voice_id"}`; `""` volta para a voz padrão |
| GET | `/narrator-voices` | Vozes disponíveis do ElevenLabs |
| POST | `/channels/:id/toggle-autopilot` | Liga ou desliga o Piloto. → `{"autopilot_enabled","can_publish"}` |
| GET | `/channels/:id/autopilot-status` | Configuração e saúde do Piloto: `videos_per_week`, `preferred_post_hour`, `consecutive_failures`, `last_failure_reason`, `last_skip_reason`, `can_publish` |
| GET | `/channels/:id/costs` | Custos por API e operação, por vídeo e por dia (30 dias), além de `gemini_free_tier` |
| GET | `/channels/:id/analytics` | Métricas agregadas (estrutura pronta, sem coleta real) |

Os canais trazem `yt_connected` e `tiktok_connected` (booleanos). Os tokens OAuth nunca são enviados.

### Contas de postagem (OAuth)

| Método | Rota | Descrição |
|---|---|---|
| GET | `/channels/:id/oauth/:provider` | `provider` = `youtube` ou `tiktok`. → `{"url"}`: abra essa URL numa janela para o usuário autorizar. `400` se o app OAuth não estiver configurado |
| DELETE | `/channels/:id/oauth/:provider` | Desconecta a conta |
| GET | `/oauth/callback` | *Público.* Retorno do Google/TikTok. Valida o `state` assinado, salva os tokens e mostra uma página HTML que avisa a janela de origem via `postMessage({type: "makinadark-oauth", ok})` |

## Vídeos

| Método | Rota | Descrição |
|---|---|---|
| GET | `/channels/:channelId/videos` | Lista os vídeos do canal, sem as cenas; cada item traz `scene_count` |
| POST | `/channels/:channelId/videos` | `{"topic"?}` (vazio = a IA escolhe um tema inédito). Responde `202` com o vídeo em `generating`; acompanhe por `GET /videos/:id` |
| GET | `/videos/:id` | Detalhe com `scenes` |
| POST | `/videos/:id/render` | Inicia a renderização. Só para `draft`/`failed` com cenas que ainda têm arquivos. `409` nos outros casos |
| POST | `/videos/:id/schedule` | `{"scheduled_for": "RFC3339"}`. Só para `ready`/`scheduled`, com uma conta conectada e horário no futuro |
| POST | `/videos/:id/unschedule` | Cancela o agendamento (`scheduled` → `ready`) |
| DELETE | `/videos/:id` | Exclui o vídeo e os arquivos. `409` enquanto estiver `generating`/`posting` |
| GET | `/calendar?from=&to=` | Vídeos agendados ou publicados de todos os canais do usuário no intervalo `[from, to)` (RFC3339; padrão: o mês atual). Itens: `id`, `channel_id`, `channel_name`, `title`, `status`, `scheduled_for`, `posted_at`, `thumbnail_path` |
| GET | `/trends?niche=` | Tendências atuais mapeadas para o nicho |

Campos principais de um vídeo:

| Campo | Significado |
|---|---|
| `status` | Ver [status](arquitetura.md#status-de-um-vídeo) |
| `render_progress`, `render_stage` | Progresso (0–100) e etapa atual enquanto `generating` |
| `error_message` | Motivo da falha ou do bloqueio. Num vídeo `posted`, é um aviso: por exemplo, uma plataforma deu certo e a outra não |
| `video_path`, `shorts_path`, `subtitles_path`, `thumbnail_path` | URL do R2 ou caminho `media/...` (servido em `/media/...`) |
| `media_purged` | `true` depois que os arquivos renderizados foram apagados por já terem sido publicados |
| `youtube_video_id`, `tiktok_publish_id` | IDs nas plataformas depois da publicação |
| `degraded_scene_count`, `total_scene_count` | Cenas que usaram conteúdo de reserva |
| `viral_score`, `hook_power_score`, `retention_score`, `ctr_score`, `originality_score`, `score_notes` | Avaliação do roteiro feita pela IA |

## Configurações

| Método | Rota | Descrição |
|---|---|---|
| GET | `/settings` | Indica o que está configurado (`*_configured`), sem nunca devolver as chaves, e a `redirect_uri` |
| PUT | `/settings` | `{"openai_api_key","gemini_api_key","elevenlabs_api_key","youtube_client_id","youtube_client_secret","tiktok_client_id","tiktok_client_secret"}`. Campos vazios mantêm o valor atual |
| GET | `/settings/test/:provider` | Testa a conexão: `groq`, `openai`, `gemini`, `claude`, `elevenlabs`, `pexels`, `pollinations`, `shotstack`, `r2`, `image_ranker`, `youtube`, `tiktok` → `{"ok","message"}` |

## Fora de `/api`

| Rota | Descrição |
|---|---|
| `GET /health` | `{"status":"healthy","time","ffmpeg":bool}`, usado pelo healthcheck do Docker |
| `GET /media/*` | Arquivos gerados quando não há R2. Aceita requisições `Range` |
