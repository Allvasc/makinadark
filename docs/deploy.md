# Deploy e operação

## Docker Compose (servidor próprio ou local)

```bash
git clone https://github.com/Allvasc/makinadark.git
cd makinadark
cp backend/.env.example backend/.env    # preencha (veja configuracao.md)
docker compose up -d --build
```

| Serviço | Container | Porta no host |
|---|---|---|
| Painel (nginx + Vue) | `makinadark-frontend` | `3000` (ou `FRONTEND_PORT`), em todas as interfaces |
| API | `makinadark-backend` | `127.0.0.1:8081` (só local; o painel chega à API pelo proxy) |
| Postgres 16 | `makinadark-db` | `127.0.0.1:5432` (só local) |
| Image ranker | `makinadark-image-ranker` | nenhuma (rede interna) |

Ordem de subida: o Postgres precisa estar saudável para o backend iniciar, e o backend precisa estar saudável para o frontend iniciar. Confira com:

```bash
docker compose ps        # todos "Up", com backend e banco "(healthy)"
```

O `image-ranker` leva um ou dois minutos para carregar o modelo CLIP. Enquanto isso, a escolha de mídia usa o primeiro resultado do Pexels.

### Recursos

- **RAM:** reserve pelo menos 8 GB para o Docker. O build do `image-ranker` (PyTorch, ~3 GB de imagem) e as renderizações FFmpeg são os maiores consumidores. Com pouca memória, o build pode travar a máquina; nesse caso, construa um serviço por vez: `docker compose build backend`, depois `frontend`, depois `image-ranker`.
- **Disco:** sem R2, cada vídeo renderizado ocupa centenas de MB no volume `backend_media` até ser publicado (depois da publicação, os arquivos renderizados são apagados).
- **HTTPS:** coloque um proxy reverso (Caddy, Traefik, o do EasyPanel…) na frente da porta do frontend. O app usa caminhos relativos e funciona em qualquer domínio.

## EasyPanel

1. Crie um serviço do tipo **Compose** apontando para este repositório e para a branch que vai a produção.
2. Na aba **Environment**, cole as variáveis no formato `CHAVE=valor`, as mesmas do `backend/.env`. O EasyPanel grava esse conteúdo num `.env` ao lado do `docker-compose.yml`, que o compose carrega automaticamente no backend.
3. Aponte o domínio para o serviço `frontend`, porta `80`.
4. Defina `REDIRECT_URI=https://<seu-domínio>/api/oauth/callback`.
5. Se o nginx do frontend não encontrar o backend pelo nome `backend` (o EasyPanel pode prefixar os hostnames com o nome do projeto, ex.: `ytd_backend`), defina `BACKEND_HOST` com o hostname interno correto.

## Configurando o OAuth

A URL de retorno é a mesma para os dois provedores: o valor de `REDIRECT_URI`, normalmente `<URL do painel>/api/oauth/callback`. Ela também aparece em **Configurações → Integrações de Postagem**.

### YouTube (Google Cloud)

1. No [Google Cloud Console](https://console.cloud.google.com/), crie um projeto e ative a **YouTube Data API v3**.
2. Configure a **tela de consentimento OAuth**. Enquanto o app estiver em modo *Testing*, adicione como *test users* as contas Google que serão conectadas.
3. Em **Credenciais → Criar credenciais → ID do cliente OAuth**, escolha o tipo **Aplicativo da Web** e adicione a URL de retorno em *URIs de redirecionamento autorizados*.
4. Copie o ID e o segredo para `YOUTUBE_CLIENT_ID` e `YOUTUBE_CLIENT_SECRET` (ou para a tela Configurações).
5. No painel, abra o canal e clique em **+ Conectar YouTube**.

Escopos pedidos: `youtube.upload` e `youtube.readonly`.

> **Limitações do Google que afetam a publicação:**
> - Apps em modo *Testing* têm refresh tokens que expiram em cerca de 7 dias. Depois disso, é preciso conectar a conta de novo.
> - Vídeos enviados pela API por projetos que **não passaram pela auditoria** do YouTube ficam travados como **privados**, mesmo que o app peça "público". Para publicar como público, o projeto precisa passar pela [auditoria de conformidade da YouTube API](https://support.google.com/youtube/contact/yt_api_form).

### TikTok (TikTok for Developers)

1. Em [developers.tiktok.com](https://developers.tiktok.com/), crie um app e adicione os produtos **Login Kit** e **Content Posting API**.
2. Solicite os escopos `user.info.basic`, `video.upload` e `video.publish`.
3. Cadastre a URL de retorno. O TikTok pode exigir HTTPS; para testar localmente, exponha o painel por um túnel (cloudflared, ngrok…) e use essa URL.
4. Copie o *Client key* e o *Client secret* para `TIKTOK_CLIENT_ID` e `TIKTOK_CLIENT_SECRET`.
5. No painel, clique em **+ Conectar TikTok** no canal.

> Apps do TikTok que ainda não passaram pela auditoria só conseguem postar com privacidade restrita. O app envia `PUBLIC_TO_EVERYONE`, e o TikTok recusa esse envio até a aprovação.

Para desconectar uma conta, clique no botão "✓ … Conectado" na página do canal.

## Atualização

```bash
git pull
docker compose up -d --build
```

O schema do banco é migrado automaticamente no start. Trabalhos que estavam em andamento durante o restart são recuperados: vídeos em geração viram `failed` e podem ser renderizados de novo; publicações interrompidas voltam para `ready` e precisam ser reagendadas.

Atualizar o Postgres de versão maior (16 → 17+) **não** é automático: exige dump e restore do volume.

## Backup e restauração

```bash
# Banco
docker exec makinadark-db pg_dump -U makina -d makinadark > backup-$(date +%F).sql

# Arquivos locais (só importa sem R2)
docker run --rm -v makina_backend_media:/data -v "$PWD":/backup alpine \
  tar czf /backup/media-$(date +%F).tgz -C /data .
```

```bash
# Restaurar o banco (com o stack no ar)
docker exec -i makinadark-db psql -U makina -d makinadark < backup-AAAA-MM-DD.sql

# Restaurar os arquivos
docker run --rm -v makina_backend_media:/data -v "$PWD":/backup alpine \
  tar xzf /backup/media-AAAA-MM-DD.tgz -C /data
```

Os nomes dos volumes levam o nome do projeto do compose como prefixo (`makina_` quando a pasta se chama `makina`). Confira com `docker volume ls`.

## Logs e diagnóstico

```bash
docker logs -f makinadark-backend      # pipeline, agendador, Piloto Automático
docker logs -f makinadark-frontend     # acessos (nginx)
```

- Para registrar cada query SQL, defina `DB_LOG_LEVEL=info`.
- **Configurações → Diagnóstico de Conexões** faz uma chamada real e mínima a cada API configurada e mostra o erro exato do provedor.
- `GET /health` no backend devolve o status e se o FFmpeg está disponível.
