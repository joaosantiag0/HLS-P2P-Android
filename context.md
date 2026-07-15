# Contexto

**Versão:** 1.0.0  
**Data:** 2026-07-14

## Estado atual
MVP implementado no monorepo: servidor Go, app Android Kotlin, protocolo documentado, Docker Compose.

## Decisões recentes
- HLS only (sem remux de `.ts` contínuo)
- Proxy/cache central no servidor (não na TV box)
- Protocolo P2P próprio (não interoperável com Novage)
- WebRTC via `stream-webrtc-android`; Views XML (sem Compose)
- Java/Kotlin minSdk 24, buffers curtos para TV box

## Próximos passos
- Validar P2P em duas TV boxes físicas (NAT/STUN/TURN)
- Medir PSS/CPU e ajustar limites de peers/cache
- TLS reverso (Caddy/nginx) em produção
- Harden ACCESS_SECRET e rate-limit de sessões

## Considerações ativas
- Emulador Android costuma falhar WebRTC por NAT
- TURN com `network_mode: host` no compose (profile `turn`)
- `DEFAULT_SERVER_URL` = `10.0.2.2:8080` (emulador); TV box precisa IP da LAN
