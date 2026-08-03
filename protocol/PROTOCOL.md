# HLS P2P Streaming Android — Protocol (MVP)

Protocolo próprio para distribuição híbrida HTTP + P2P de segmentos HLS.
Não é interoperável com Novage/p2p-media-loader.

## Identificadores

| Campo | Formato | Descrição |
|-------|---------|-----------|
| `streamKey` | string | Swarm estável: Xtream `host|type|streamId|variant` ou `opaque:<sha256(url)>` |
| `segmentId` | hex sha256 | Conteúdo do segmento (content-addressed) |
| `peerId` | uuid v4 | Identidade do cliente na sessão |
| `sessionId` | uuid v4 | Sessão HTTP no servidor (proxy HLS) |

## API HTTP (servidor)

### `POST /api/v1/sessions`
```json
{
  "url": "https://provider/.../live/.../....m3u8",
  "userAgent": "VLC/3.0.0"
}
```
Resposta:
```json
{
  "sessionId": "...",
  "streamKey": "...",
  "manifestUrl": "/hls/{sessionId}/index.m3u8",
  "wsUrl": "/ws",
  "iceServers": [{"urls": ["stun:stun.l.google.com:19302"]}]
}
```

### `GET /hls/{sessionId}/index.m3u8`
Playlist reescrita. Segmentos apontam para `/hls/{sessionId}/seg/{segmentId}.ts`.

### `GET /hls/{sessionId}/seg/{segmentId}.ts`
Segmento em cache (bytes originais, sem remux).

### `GET /healthz`
Healthcheck.

## Sinalização WebSocket (`/ws`)

Cliente envia após conectar:
```json
{"type":"hello","peerId":"...","streamKey":"...","sessionId":"..."}
```

Mensagens:

| type | direção | payload |
|------|---------|---------|
| `peers` | server→client | `{peers:[{peerId}]}` |
| `peer-joined` | server→client | `{peerId}` |
| `peer-left` | server→client | `{peerId}` |
| `signal` | bidirecional | `{to,from,sdpType,sdp,candidate}` |
| `inventory` | peer→peers (via relay opcional) | `{segmentIds:[...]}` |
| `ping` / `pong` | keep-alive | `{}` |

Sinalização WebRTC usa `signal` com `sdpType` ∈ `offer|answer|candidate`.

## DataChannel P2P

Canal: `p2p-segments`, ordered=false, maxRetransmits=2.

Mensagens binárias (JSON UTF-8 em frames curtos; payload de segmento em chunks):

### Controle (JSON)
```json
{"t":"have","ids":["abc..."]}
{"t":"want","id":"abc...","offset":0}
{"t":"chunk","id":"abc...","offset":0,"total":12345,"data":"<base64>"}
{"t":"done","id":"abc..."}
{"t":"nack","id":"abc...","reason":"missing"}
```

Chunks de no máximo 16 KiB. Receptor valida SHA-256 do segmento completo antes de usar.

## Política híbrida

1. Startup e playhead próximo: preferir HTTP do proxy.
2. Se peer tiver o `segmentId` e timeout P2P < limite: baixar via DataChannel.
3. Timeout, hash inválido ou peer ausente → fallback HTTP imediato.
4. Limites TV box: ≤3 peers, ≤2 transfers paralelas, cache circular em disco.

## Contadores

| Métrica | Conta |
|---------|-------|
| HTTP ↓ | bytes de segmentos/manifest via HTTP |
| HTTP ↑ | bytes de sinalização/controle WebSocket |
| P2P ↓ | bytes de chunks recebidos no DataChannel |
| P2P ↑ | bytes de chunks enviados no DataChannel |
