# Solução de problemas

Comece por **Configurações → Diagnóstico de Conexões → Testar Todas**: cada teste faz uma chamada real à API e mostra o erro exato do provedor. Para o resto, veja os logs com `docker logs -f makinadark-backend`.

## Geração

| Sintoma | Causa provável | O que fazer |
|---|---|---|
| "nenhuma chave de API de LLM configurada" | Nenhuma chave de LLM chegou ao backend | Preencha `GROQ_API_KEY`, `GEMINI_API_KEY`, `ANTHROPIC_API_KEY` ou `OPENAI_API_KEY` no `backend/.env` (ou no `.env` do EasyPanel) e rode `docker compose up -d backend`. Para conferir sem mostrar valores: `docker exec makinadark-backend sh -c 'printenv OPENAI_API_KEY \| wc -c'` (1 = vazia) |
| "todos os provedores de LLM falharam - Groq: … \| Gemini: …" | Chaves inválidas, sem crédito ou com cota esgotada | A mensagem traz o motivo de cada provedor; corrija o que estiver indicado |
| Logo/capa falha com "falha ao gerar imagem (logo) - OpenAI: … status 400/401/429" | Chave da OpenAI inválida, sem créditos, organização não verificada para gpt-image ou limite de requisições | Veja o detalhe da mensagem. Para baixar o custo, defina `OPENAI_IMAGE_QUALITY=medium` |
| Logo/capa de baixa qualidade | Nem OpenAI nem Gemini configurados, então caiu no Pollinations | Configure `OPENAI_API_KEY` e regere em **Regerar Logo / Regerar Capa** |
| Vídeo para em "Gerando" e vira "Falhou: … interrompida inesperadamente" | O backend reiniciou durante o processo (deploy, falta de memória) | Clique em **Tentar Renderizar Novamente** se o roteiro já existia; senão, exclua e gere de novo |
| Aviso "X de Y cenas usaram conteúdo de reserva" | Faltou narração (TTS falhou) ou imagem/clipe para algumas cenas | Confira ElevenLabs, OpenAI e Pexels no diagnóstico. Acima de 30%, a publicação é bloqueada |
| A narração gratuita corta o texto | Caiu no TTS gratuito do Google, que só lê 200 caracteres | Configure `ELEVENLABS_API_KEY` ou `OPENAI_API_KEY` |
| "Os arquivos das cenas já foram removidos…" ao renderizar | Depois de uma renderização bem-sucedida, os arquivos de origem são apagados para economizar espaço | Gere um vídeo novo |

## Publicação

| Sintoma | Causa provável | O que fazer |
|---|---|---|
| "Conecte o YouTube ou o TikTok deste canal antes de agendar" | O canal não tem conta vinculada | Página do canal → **+ Conectar YouTube/TikTok** |
| "YouTube OAuth não configurado" ao conectar | `YOUTUBE_CLIENT_ID`/`SECRET` vazios | Configure (veja [deploy.md](deploy.md#configurando-o-oauth)) |
| Google mostra "redirect_uri_mismatch" | `REDIRECT_URI` diferente do cadastrado no console | Deixe os dois **idênticos** (protocolo, domínio, porta e caminho `/api/oauth/callback`) |
| A janela de autorização diz "Link de autorização expirado" | Passaram mais de 15 min entre clicar em conectar e autorizar, ou o `JWT_SECRET` mudou nesse meio-tempo | Clique em conectar de novo |
| O badge "Conectado" não atualiza depois de autorizar | O callback roda em outra origem (`REDIRECT_URI` apontando para a porta do backend) | Recarregue a página. O ideal é usar `<URL do painel>/api/oauth/callback` |
| Vídeo volta para "Pronto" com "Bloqueado antes de publicar: vídeo final tem apenas Ns" | A trava de qualidade exige pelo menos 120 s | Gere um roteiro mais longo |
| Vídeo volta para "Pronto" com "Falha ao publicar - YouTube: failed to refresh…" | O refresh token expirou ou foi revogado (apps em modo *Testing* expiram em cerca de 7 dias) | Desconecte e conecte a conta de novo |
| Vídeo publicado no YouTube aparece como **privado** | O projeto do Google não passou pela auditoria da API do YouTube | Solicite a auditoria (veja [deploy.md](deploy.md#youtube-google-cloud)) |
| TikTok recusa com erro de app não auditado ou privacidade | O app do TikTok ainda não foi aprovado para posts públicos | Conclua a auditoria do app no TikTok for Developers |
| "Publicação interrompida (reinício do servidor…)" | O backend reiniciou durante o upload | Agende de novo |

## Piloto Automático

| Mensagem no canal | Significado |
|---|---|
| "Pausado até você conectar o YouTube ou o TikTok" | Sem conta conectada, o Piloto não produz nada (para não gastar créditos) |
| "Fila cheia: N vídeo(s) aguardando publicação" | Já há 2 vídeos gerando ou agendados; o próximo sai quando a fila andar |
| "Limite semanal atingido (N/M…)" | Já foram criados `videos_per_week` vídeos nos últimos 7 dias; contam também os que falharam |
| "Auto-Pilot falhou N vezes seguidas" | Cada falha seguida dobra a espera até a próxima tentativa (até 6 h). O motivo aparece logo abaixo |

## Infraestrutura

| Sintoma | Causa provável | O que fazer |
|---|---|---|
| Painel abre, mas tudo dá erro 502 | O backend não está no ar ou o nginx não acha o hostname `backend` | `docker compose ps`; no EasyPanel, ajuste `BACKEND_HOST` |
| Todos são deslogados a cada deploy | `JWT_SECRET` vazio, então um segredo aleatório é gerado a cada start | Defina `JWT_SECRET` fixo |
| A máquina trava ou o Docker cai durante o `docker compose build` | Falta de memória; o `image-ranker` (PyTorch) é o build mais pesado | Aumente a RAM do Docker ou construa um serviço por vez (veja [deploy.md](deploy.md#recursos)) |
| A prévia do vídeo não permite avançar ou voltar | Proxy sem suporte a `Range` | O nginx do projeto já repassa `Range`; se houver outro proxy na frente, habilite o repasse |
| Depois de um deploy o painel continua com a versão antiga | Cache do navegador ou de uma CDN guardando o `index.html` | O nginx envia `Cache-Control: no-cache` no `index.html`; se houver uma CDN na frente, limpe o cache dela |
| Não consigo criar conta | O cadastro está desativado por padrão | Use a conta admin ou defina `ALLOW_REGISTRATION=true` |
| Esqueci a senha do admin | – | Gere um hash e atualize o banco: `docker exec -it makinadark-db psql -U makina -d makinadark -c "UPDATE users SET password_hash='<hash bcrypt>' WHERE email='admin@makinadark.com';"`. Para gerar o hash, use, por exemplo, `htpasswd -bnBC 10 "" 'nova-senha' \| tr -d ':\n'` |
