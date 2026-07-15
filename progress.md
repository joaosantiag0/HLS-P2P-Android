# Progresso

**Versão:** 1.0.0  
**Data:** 2026-07-14

## Feito
- [x] Monorepo (Android, server, protocol, deploy, memory-bank)
- [x] Servidor: sessões, proxy/cache HLS, SSRF, sinalização WS
- [x] Testes Go (hls, cache, ssrf)
- [x] App: formulário URL/UA/servidor, Media3, overlay HTTP/P2P
- [x] PeerMesh WebRTC DataChannel + fallback HTTP
- [x] Docker Compose + README

## Backlog
- [ ] Spike medido em 2 TV boxes (RAM/CPU/estabilidade)
- [ ] Testes de integração servidor com fixture HLS local
- [ ] Rate-limit e autenticação forte de API
- [ ] UI Leanback dedicada (além de foco D-pad atual)

## Issues conhecidos
- P2P em emulador instável (NAT)
- Compose TURN usa profile separado / host networking
- `onStop` derruba peers (tradeoff memória TV box)
