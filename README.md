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
   - Deploy.

### Atualizar depois

Só dar push na branch que o Coolify observa — ele builda e reimplanta
automaticamente (ou clique em "Redeploy" no painel, se o auto-deploy por push
não estiver ligado).

```bash
git add -A
git commit -m "sua mensagem"
git push
```

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
main.go               servidor HTTP + rotas /api/photos, /api/upload, /api/chat
internal/store/       persistência em JSON (mutex + escrita atômica)
web/index.html         site (embutido no binário via go:embed)
Dockerfile             build multi-stage (Go alpine → alpine runtime)
```
