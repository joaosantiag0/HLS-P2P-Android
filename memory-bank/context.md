# Contexto

**Versão:** 1.1.0  
**Data:** 2026-07-14

## Estado atual
MVP funcionando nos 2 emuladores Genymotion (`127.0.0.1:6555` / `:6562`): HTTP + P2P via **relay WebSocket** (WebRTC falha no emulador por NAT).

## Decisões recentes
- HLS only (sem remux de `.ts` contínuo)
- Proxy/cache central no servidor (não na TV box)
- Protocolo P2P próprio (não interoperável com Novage)
- Emulador: P2P = relay WS pelo tracker; TV box real: DataChannel direto
- Inventário = 40 IDs mais recentes (url-key `/u/{sha256}`)
- Live: jitter + espera ~2.5s pelo inventário do peer antes do HTTP

## Correções 2026-07-14
- WS: dispatch/relay fora da thread OkHttp (evitava `signaling closed`)
- Go: `offset` sem `omitempty` (crash `JSONException: No value for offset`)
- Live race: espera + jitter para um peer seedar e o outro puxar via relay

## Próximos passos
- Validar DataChannel em duas TV boxes físicas (NAT/STUN/TURN)
- Medir PSS/CPU e ajustar limites de peers/cache
- TLS reverso (Caddy/nginx) em produção

## Considerações ativas
- `DEFAULT_SERVER_URL` = `http://192.168.2.29:8080`
- Autoplay de teste: `am start … --es url "…m3u8" --es server "http://…"`
