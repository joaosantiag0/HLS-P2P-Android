# HLS P2P Streaming Android

## Visão geral
Sistema de streaming live HLS com distribuição híbrida HTTP + P2P para TV boxes Android de baixo recurso.

## Objetivos
- Reproduzir URLs Xtream Codes no formato HLS (`.m3u8`)
- Reduzir carga na origem via troca de segmentos entre peers
- Rodar nativamente sem WebView
- Exibir métricas de download/upload HTTP e P2P sobre o player

## Requisitos core
- App Android TV nativo (Media3)
- Servidor monolítico (proxy/cache HLS + sinalização)
- Protocolo P2P próprio sobre WebRTC DataChannel
- Fallback HTTP sempre disponível

## Fora de escopo (MVP)
- Remux/transcode de MPEG-TS contínuo
- DRM
- Interoperabilidade com peers Novage/WebTorrent web
- Compatibilidade com WebView

## Versão
v1.0.0
