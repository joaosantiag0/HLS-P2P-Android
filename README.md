# P2PPlayer

Streaming **HLS híbrido** (HTTP + P2P) para TV boxes Android com pouca RAM/CPU.

- **Sem WebView** — Media3/ExoPlayer + WebRTC DataChannel nativos
- **Sem remux/transcode** — proxy/cache content-addressed dos `.ts` originais
- **Protocolo próprio** — inspirado no [p2p-media-loader](https://github.com/Novage/p2p-media-loader), sem interoperabilidade obrigatória

```bash
git clone https://github.com/StgTech/p2p.git
cd p2p
```

## Estrutura

```
Android/      App TV (Kotlin, Media3, WebRTC)
server/       Servidor Go (API, proxy HLS, sinalização WS, cache)
protocol/     Contrato de mensagens e IDs
deploy/       Docker Compose + Dockerfile
memory-bank/  Contexto do projeto
```

## Requisitos

- Go 1.24+
- Android Studio / JDK 17+ (Gradle 8.13)
- Docker (opcional)
- TV box ou emulador com rede real (Genymotion: WebRTC precisa STUN/TURN; DataChannel direto pode falhar sem TURN)

## Servidor

```bash
cd server
go run ./cmd/server
# escuta :8080
```

Ou com Docker:

```bash
cd deploy
docker compose up --build server
```

TURN opcional:

```bash
TURN_USER=p2p TURN_PASS=segredo docker compose --profile turn up --build
```

Variáveis: `ACCESS_SECRET`, `ALLOW_PRIVATE_NET`, `CACHE_MAX_BYTES`, `STUN_URLS`, `TURN_*`.

## Painel web (swarm)

```
http://192.168.2.29:8080/dashboard
```

Peers online ao vivo. O grafo de bytes só mostra fluxos se houver telemetria futura —
**segmentos P2P vão no DataChannel** e não atravessam o servidor.

## WebRTC: STUN e TURN

Por padrão usa **TURN público Open Relay** (Metered):

- `turn:openrelay.metered.ca` + user/pass `openrelayproject`
- STUN Google + `stun.relay.metered.ca`

Opcional (mais estável, 20 GB/mês grátis): crie conta em [Open Relay](https://www.metered.ca/tools/openrelay/) e defina:

```bash
METERED_API_KEY=sua_chave
```

TURN local (coturn) continua disponível:

```bash
TURN_URL=turn:192.168.2.29:3478 TURN_USER=p2p TURN_PASS=p2psecret \
  docker compose --profile turn up -d
```

Sinalização em `ws://…/ws` (hello/peers/signal/inventory).  
Segmentos P2P só no **DataChannel**.

## App Android

1. Abra a pasta `Android/` no Android Studio.
2. No formulário:
   - **URL do servidor** — padrão `http://192.168.2.29:8080` (na LAN; use `10.0.2.2:8080` só no emulador AVD)
   - **URL do stream** — HLS (`.m3u8`)
   - **User-Agent** — enviado à origem via proxy
3. Overlay: `HTTP ↓/↑`, `P2P ↓/↑` e contagem de peers.

Autoplay via ADB (teste):

```bash
adb shell am start -n br.com.p2pplayer/.MainActivity \
  --es server "http://192.168.2.29:8080" \
  --es url "https://exemplo.com/playlist.m3u8" \
  --ez autoplay true
```

## Fluxo

1. App cria sessão `POST /api/v1/sessions` com URL + UA.
2. Servidor baixa playlist, escolhe variante, reescreve segmentos como `/hls/{session}/u/{urlKey}.ts`.
3. Playback: store local → **WebRTC DataChannel** → HTTP.
4. Sinalização em `/ws`; STUN/TURN via sessão (`iceServers`).

Detalhes: [protocol/PROTOCOL.md](protocol/PROTOCOL.md).

## Testes

```bash
cd server && go test ./...
cd Android && ./gradlew :app:testDebugUnitTest
```

## Limites TV box (MVP)

- ≤ 3 peers, ≤ 2 transfers P2P paralelas
- Buffer Media3 curto (~2.5–10 s)
- Cache local de segmentos ~64 MiB
- P2P desligado em `onStop` (background)

## Segurança

- SSRF: só http(s); IPs privados bloqueados por padrão (`ALLOW_PRIVATE_NET`)
- Credenciais Xtream não entram em `streamKey`/logs de path
- `ACCESS_SECRET` opcional via header `X-Access-Secret` / query `access=`

## Capacidade: 3000 canais / 2000 clientes

Medição local (Docker, ~2 clientes no JMV ~3 Mbps): processo **~30 MB RAM**, CPU baixa; cache default **2 GB** enche rápido (~1600 segs). O gargalo em escala é **banda + disco de cache**, não CPU do Go.

### Premissas

| Item | Valor usado |
|------|-------------|
| Bitrate médio HLS | **3–4 Mbps** (~1,2–1,5 MB / 4 s) |
| Clientes concurrentes | **2000** |
| Catálogo | **3000** canais (nem todos ativos ao mesmo tempo) |
| Papel do server | proxy/cache HLS + sinalização WS (**sem** relay de mídia P2P) |
| P2P | DataChannel; reduz HTTP no server nos canais com ≥2 peers |

Fórmula egress (pior caso, sem P2P):

```
egress_Gbps ≈ clientes × bitrate_Mbps / 1000
           ≈ 2000 × 4 / 1000 = 8 Gbps
```

### Planilha de banda (egress para clientes)

| Hit HTTP no proxy* | Egress estimado | Ingress origem (ordem) |
|--------------------|-----------------|-------------------------|
| 100% (sem P2P / cache frio) | **6–8 Gbps** | ~igual ao egress |
| 50% (P2P médio nos canais quentes) | **3–5 Gbps** | bem menor (cache + swarm) |
| 20–30% (P2P forte no top N) | **1,5–3 Gbps** | seed + cauda longa |

\*Fração do tráfego de segmento que ainda sai do server (HTTP). Playlist/sinalização são desprezíveis vs `.ts`.

### Cache em disco (janela live ~60 s)

```
MB/canal_ativo ≈ (60 / 4) × 1,3 ≈ 20 MB
```

| Canais ativos simultâneos | Cache útil |
|---------------------------|------------|
| 200 | ~4 GB |
| 500 | ~10–15 GB |
| 1000 | ~20–30 GB |
| Margem produção | **100–200 GB** NVMe |

Default `CACHE_MAX_BYTES=2GiB` é só lab — em produção subir para ≥100 GB.

### RAM / CPU

| Recurso | Estimativa 2000 clientes |
|---------|---------------------------|
| RAM processo + buffers de `.ts` | **4–8 GB** (pico se muitos `Read` paralelos) |
| CPU | **4–8 vCPU** (proxy + TLS + rewrite de playlist) |
| WS (sinalização) | barato frente ao HLS |

### Servidor recomendado

| Perfil | Spec | Link | Cache |
|--------|------|------|-------|
| Piloto (≤500 clientes) | 4 vCPU / 8 GB | 1–2 Gbps | 20–50 GB |
| **Produção 2000 / 3000** | **8 vCPU / 16 GB** | **5–10 Gbps** | **100–200 GB** NVMe |
| Conservador (P2P fraco) | 16 vCPU / 32 GB | 10 Gbps+ ou 2–3 nós | 200–500 GB |

Exemplos: dedicated 10 Gbps (Hetzner/OVH) costuma sair mais barato que cloud hyperscaler por causa do **egress**.

TURN: Open Relay free **não** aguenta essa escala — Metered pago ou **coturn** próprio (`docker compose --profile turn`).

### Checklist antes de produção

1. `CACHE_MAX_BYTES` ≥ 100 GB e volume em SSD/NVMe  
2. Subir `MaxIdleConns` / pool HTTP à origem (hoje 32 — insuficiente para milhares de clientes)  
3. Preferir streaming de segmento (`io.Copy`) em vez de carregar `.ts` inteiro na RAM  
4. Rate-limit + `ACCESS_SECRET`  
5. Métricas: egress Gbps, cache hit, sessões, tamanho de swarm por `streamKey`  
6. 2+ réplicas atrás de LB se um único NIC não cobrir a banda  

### Exemplo rápido (planilha mental)

```
clientes=2000  bitrate=4Mbps  p2p_offload=50%
egress ≈ 2000×4×0.50 = 4000 Mbps ≈ 4 Gbps  → link 5–10 Gbps
canais_ativos≈400 → cache ≈ 400×20MB ≈ 8 GB (+ folga → 100 GB)
máquina: 8 vCPU / 16 GB RAM / NVMe
```
