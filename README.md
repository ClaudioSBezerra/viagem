# viagem — planejador de roteiros

Monte um roteiro (cidades + noites em cada uma), diga o período em que a
viagem pode acontecer, e o app testa várias janelas de data dentro desse
período cotando, para cada uma, o voo de ida e volta mais um hotel por
cidade — para mostrar qual semana sai mais barata. Os roteiros ficam salvos
e nomeados ("Miami em maio/2027", "Buenos Aires no feriado"), e cada
sugestão pode ser impressa ou salva em PDF.

Um único binário Go: o HTML fica embutido no binário (`go:embed`) e os
roteiros ficam em `data/trip.json`, dentro de um volume Docker persistente em
produção.

Deploy: **Coolify**, no mesmo VPS (76.13.171.196) e domínio (`fbtax.cloud`) do
FAROL e do SMARTPICK — em `viagem.fbtax.cloud`.

## Rodar localmente

```bash
go run .
# abre em http://127.0.0.1:8080
```

Variáveis de ambiente:

- `ADDR` — endereço:porta para escutar (padrão `127.0.0.1:8080`)
- `DB_PATH` — caminho do arquivo de dados (padrão `data/trip.json`)
- `SERPAPI_KEY` — chave do [SerpApi](https://serpapi.com/). Sem ela, os
  roteiros podem ser montados, salvos e editados normalmente, mas não
  cotados: o botão "Cotar voo e hotéis" nem aparece.

## Como a cotação funciona

- As cidades são **trechos consecutivos de uma viagem só**, na ordem da
  lista: a cidade 1 faz check-in no dia da ida, a 2 entra no dia em que a 1
  sai, e o último check-out cai no dia da volta. Cada hotel é cotado só nas
  noites daquela cidade.
- O **total de cada janela** é o voo somado a **todos** os hotéis. Se
  qualquer preço faltar, a janela mostra "—" em vez de um total incompleto.
- Hotéis considerados: até 4★, com café da manhã e estacionamento
  incluídos; o mais barato que atende os filtros vence.
- **Cota:** cada rodada gasta no máximo 24 buscas no SerpApi (1 voo + 1 hotel
  por cidade, para cada janela de data). Roteiros com mais cidades comparam
  menos janelas: 1–2 cidades → 8 janelas, 3 → 6, 4–5 → 4, 6–7 → 3,
  8–10 → 2. A cota gasta por cotação não aumenta com o número de cidades.
- **Voo de volta a partir da última cidade (opcional):** se a viagem chega
  num aeroporto e sai de outro — ex.: chega em Miami, roda o roteiro, mas
  volta de Orlando —, preencha "Voltando de" com o aeroporto da última
  cidade. O app cota esse trecho **à parte**, um voo só de ida da última
  cidade de volta pra origem, na data em que o roteiro termina; ele aparece
  junto do resultado de cada janela mas **não entra no total** (o total
  continua sendo ida-e-volta + hotéis, pra não misturar dois jeitos
  diferentes de voltar pra casa). Deixe em branco pra manter o
  comportamento de sempre (voo de ida e volta só entre origem e destino).
  Como é mais uma busca por janela, roteiros com voo de volta comparam
  menos janelas de data pelo mesmo teto de cota.
- Uma rodada por vez, com **cooldown de 3h** entre rodadas — global, não por
  roteiro, porque todos gastam da mesma cota mensal. Nada roda sozinho: só
  cota quando alguém clica.
- Editar cidades, datas, aeroportos (incluindo o de volta) ou passageiros de
  um roteiro descarta a cotação anterior (renomear não).

Limites: até 10 cidades por roteiro, 2 a 21 noites no total, 1 a 9
passageiros, até 20 roteiros salvos.

## PDF / impressão

O botão "🖨️ Baixar PDF / imprimir esta sugestão" aparece abaixo do resultado
de um roteiro cotado. Ele só chama `window.print()` — o navegador já sabe
salvar como PDF — e a folha de impressão mostra apenas a sugestão daquele
roteiro, com cabeçalho próprio (nome, aeroportos, período, roteiro, data da
cotação e critérios dos hotéis).

## API

| Método | Rota | O quê |
|---|---|---|
| `GET` | `/api/config` | se a cotação está ligada neste servidor |
| `GET` | `/api/trips` | lista os roteiros (mais novo primeiro) |
| `POST` | `/api/trips` | cria um roteiro |
| `GET` | `/api/trips/{id}` | um roteiro, com a cotação dele (a página faz polling aqui) |
| `PUT` | `/api/trips/{id}` | edita um roteiro |
| `DELETE` | `/api/trips/{id}` | apaga um roteiro e a cotação dele |
| `POST` | `/api/trips/{id}/search` | inicia a cotação (roda em segundo plano) |

## Deploy via Coolify

O repositório inclui só um `Dockerfile` — app de container único (sem banco,
sem cache), então o build pack **Dockerfile** do Coolify é suficiente.

1. **Push pro GitHub** (repo: `ClaudioSBezerra/viagem`).
2. **No painel do Coolify**:
   - New Resource → Application → conectar ao repo `ClaudioSBezerra/viagem`.
   - Build Pack: **Dockerfile**.
   - Port: `8080`.
   - Domain (FQDN): `viagem.fbtax.cloud` (A record apontando pra
     `76.13.171.196`).
   - Aba **Storages**: volume persistente com mount path `/data` — é onde o
     app grava `trip.json`. Sem isso, os roteiros somem a cada redeploy.
   - Aba **Environment Variables**: `SERPAPI_KEY`.
   - Deploy.

### Atualizando um deploy da versão anterior

A versão anterior deste app (roteiro Ibérico, galeria de fotos, chat) usava o
mesmo `trip.json`. As fotos e mensagens antigas **não aparecem mais**, mas
continuam preservadas dentro do arquivo a cada gravação, e os arquivos de
foto em `/data/uploads/` não são tocados. As variáveis `GOOGLE_*` e
`QUOTES_ENABLED` podem ser removidas do Coolify.

## Backup dos dados

```bash
docker cp $(docker ps -qf name=viagem):/data ./backup-viagem-$(date +%F)
```
(rode direto no VPS, via SSH)

## Proteção de acesso (opcional)

Não há login: qualquer pessoa com o link pode criar, editar e apagar
roteiros, e disparar cotações (que gastam cota). Para restringir ao grupo, o
Coolify tem um campo de Basic Auth pronto na aba de configurações do app.

## Estrutura

```
main.go                 wiring: store, fetchers, servidor HTTP
server.go               rotas e /api/config
handlers_trips.go       CRUD de roteiros + início da cotação
refresh.go              o buscador: encadeia as estadias e cota cada janela
job.go                  trava "uma rodada por vez + cooldown"
httputil.go             helpers de JSON/HTTP
internal/trip/          regras do roteiro: cidades, janelas de data, estadias
internal/store/         persistência em JSON (mutex + escrita atômica)
internal/quotes/        hotel mais barato por cidade (SerpApi/Google Hotels)
internal/flights/       voo de ida e volta (SerpApi/Google Flights)
web/index.html          a página (embutida no binário via go:embed)
Dockerfile              build multi-stage (Go alpine → alpine runtime)
```
