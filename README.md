# Roteiro Ibérico 2026 — micro site da viagem

Site estático (roteiro dia a dia, hotéis, rotas) + galeria de fotos (upload
direto do celular/computador) e chat compartilhado entre os 7 passageiros. Um
único binário Go, sem dependências externas: o HTML fica embutido no binário
(`go:embed`), as fotos enviadas ficam em `data/uploads/` e os metadados
(fotos/chat) em `data/trip.json` — tudo dentro do mesmo volume Docker
persistente em produção.

Deploy: **Coolify**, no mesmo VPS (76.13.171.196) e domínio (`fbtax.cloud`) do
FAROL e do SMARTPICK — em `viagem.fbtax.cloud`.

## Rodar localmente

```bash
go run .
# abre em http://127.0.0.1:8080
```

Variáveis de ambiente opcionais:

- `ADDR` — endereço:porta para escutar (padrão `127.0.0.1:8080`)
- `DB_PATH` — caminho do arquivo de dados (padrão `data/trip.json`)

## Deploy via Coolify

O repositório inclui só um `Dockerfile` — app de container único (sem banco,
sem cache), então o build pack **Dockerfile** do Coolify é suficiente: ele
mesmo gera as labels do Traefik, a rede e o certificado TLS a partir do que
você configurar na UI (diferente do FAROL/SMARTPICK, que usam Docker Compose
por terem vários serviços — api, db, redis — na mesma stack).

1. **Push pro GitHub** (repo: `ClaudioSBezerra/viagem`):
   ```bash
   git push -u origin main
   ```

2. **No painel do Coolify**:
   - New Resource → Application → conectar ao repo `ClaudioSBezerra/viagem`.
   - Build Pack: **Dockerfile**.
   - Port: `8080` (é o que o `EXPOSE`/`ADDR` do Dockerfile usam).
   - Domain (FQDN): `viagem.fbtax.cloud` — confirme que o DNS (A record)
     desse subdomínio aponta pro IP do servidor `76.13.171.196`, se ainda não
     estiver.
   - Aba **Storages**: adicione um volume persistente com mount path `/data`
     (é onde o app grava `trip.json` e as fotos enviadas — sem isso, tudo
     some a cada redeploy).
   - Aba **Environment Variables**: adicione as 4 variáveis do Google Drive
     (veja seção abaixo) se quiser sincronização automática das fotos.
   - Deploy.

## Sincronização com o Google Drive (opcional)

Toda foto enviada pelo site pode ser copiada automaticamente pra uma pasta do
Google Drive, além de ficar salva no próprio servidor. Isso usa OAuth com a
conta Google dona da pasta (contas pessoais @gmail.com não dão quota de
armazenamento pra Service Account gravar em Meu Drive, então tem que ser
assim).

### Setup (uma vez só)

1. No [Google Cloud Console](https://console.cloud.google.com/), crie um
   projeto, ative a **Google Drive API**, configure a **OAuth consent
   screen** (tipo External, adicione sua conta como test user, e depois mude
   o **Publishing status** para **"In production"** — senão o token expira
   em 7 dias).
2. Em **Credentials**, crie um **OAuth client ID** tipo **Desktop app**.
   Guarde o Client ID e o Client Secret.
3. Rode localmente (uma vez só, abre uma autorização no navegador):
   ```bash
   GOOGLE_CLIENT_ID=seu-client-id \
   GOOGLE_CLIENT_SECRET=seu-client-secret \
   go run ./cmd/get-drive-token
   ```
   Abra o link impresso, autorize com a conta dona da pasta do Drive, e
   copie o **refresh token** que aparece no terminal.
4. Pegue o **ID da pasta** do Drive a partir da URL dela:
   `https://drive.google.com/drive/folders/`**`ESSE_ID_AQUI`**`?usp=drive_link`

### Variáveis de ambiente (Coolify)

| Variável | Valor |
|---|---|
| `GOOGLE_CLIENT_ID` | do passo 2 |
| `GOOGLE_CLIENT_SECRET` | do passo 2 |
| `GOOGLE_REFRESH_TOKEN` | do passo 3 |
| `GOOGLE_DRIVE_FOLDER_ID` | do passo 4 |

Se qualquer uma dessas 4 faltar, o app funciona normalmente (galeria só no
próprio servidor) — a sincronização é best-effort: se o Drive falhar por
qualquer motivo, só fica registrado no log, nunca quebra o upload no site.

### Atualizar depois

Só dar push na branch que o Coolify observa — ele builda e reimplanta
automaticamente (ou clique em "Redeploy" no painel, se o auto-deploy por push
não estiver ligado).

```bash
git add -A
git commit -m "sua mensagem"
git push
```

## Cotação automática dos hotéis

O servidor busca sozinho o preço de cada hospedagem do roteiro na página de
busca do Booking.com (2 adultos, 1 quarto, nas datas do card) e guarda o
resultado no `trip.json`. A página lê esse cache e mostra o preço junto dos
botões, com a hora em que foi capturado — preço de hotel envelhece rápido, e
uma cotação sem data seria pior que nenhuma.

Se a busca falhar (site fora do ar, layout novo, bloqueio anti-bot), o card
mostra "sem cotação" e o botão de link continua funcionando exatamente como
antes — nada se perde.

- Atualiza sozinho a cada 12h, começando 15s depois que o app sobe.
- Botão **"Atualizar cotações"** no topo força uma rodada (limite: 1 a cada
  10 min).
- As 8 hospedagens são buscadas com 5s de intervalo entre elas.

| Variável | Efeito |
|---|---|
| `QUOTES_ENABLED=0` | desliga a cotação; o site volta a ser só links |

### Quando o preço parar de aparecer

O Booking muda o HTML de tempos em tempos. O parser tenta várias estratégias
em ordem (`data-testid` exato → qualquer `data-testid` com "price" →
`aria-label` → texto puro), mas se todas falharem, use o endpoint de
diagnóstico pra ver o que chegou:

```bash
curl -s 'https://viagem.fbtax.cloud/api/quotes/debug?id=lisboa' | head -c 2000
```

Ele mostra o tamanho da resposta, se veio página de captcha, qual estratégia
casou (se alguma) e o começo do HTML. O campo `id` aceita só os IDs do
roteiro (`lisboa`, `porto`, `coruna`, `madrid`, `granada`, `torremolinos`,
`sevilha`, `salamanca`) — nunca uma URL arbitrária, pra não transformar o
servidor em proxy aberto.

> As datas das hospedagens vivem em dois lugares: nos cards do
> `web/index.html` e na tabela `Stays` do `internal/quotes/trip.go`. Um teste
> (`go test ./internal/quotes/`) falha se as duas listas divergirem, então
> ajuste sempre as duas.

## Backup dos dados

```bash
docker cp $(docker ps -qf name=viagem):/data ./backup-viagem-$(date +%F)
```
(rode direto no VPS, via SSH — copia `trip.json` e a pasta `uploads/` inteira)

## Proteção de acesso (opcional)

Como não há login, qualquer pessoa com o link de `viagem.fbtax.cloud` pode
postar fotos/mensagens. Se quiser restringir ao grupo, o Coolify tem um campo
de Basic Auth pronto na aba de configurações do app (nome de usuário/senha
únicos, sem precisar mexer em Traefik na mão).

## Estrutura

```
main.go                    servidor HTTP + rotas /api/photos, /api/upload, /api/chat
internal/store/            persistência em JSON (mutex + escrita atômica)
internal/drivesync/        upload best-effort pro Google Drive (OAuth)
cmd/get-drive-token/       helper local pra gerar o refresh token (rodar 1x)
web/index.html             site (embutido no binário via go:embed)
Dockerfile                 build multi-stage (Go alpine → alpine runtime)
```
