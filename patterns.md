# Padrões

**Versão:** 1.0.0

## Arquitetura
```
Origem HLS → Servidor Go (sessão, rewrite, cache SHA-256, WS)
                ↕ HTTP fallback
App Media3 ← HybridDataSource ← PeerMesh (WebRTC DataChannel)
```

## Servidor (Go)
- Pacotes: `api`, `session`, `hls`, `cache`, `signaling`, `ssrf`, `config`
- Um binário: `cmd/server`
- Segmentos content-addressed; manifest reescrito para `/hls/{session}/seg/{sha}.ts`
- `streamKey` Xtream: `host|kind|streamId|variant` (sem user/pass)

## Android
- Kotlin + View Binding + Media3 HLS
- `HybridDataSource` tenta store local → P2P → HTTP
- `TrafficStats` centraliza contadores (evita dupla contagem)
- Lifecycle: libera WebRTC em `onStop`, reconecta em `onStart`

## Protocolo
Ver `protocol/PROTOCOL.md`.
