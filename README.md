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
- TV box ou emulador com rede real (em Genymotion o WebRTC costuma falhar; o app usa relay WS como fallback)

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
