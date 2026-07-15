# Orçamento de recursos — TV box de referência

Preencher após spike em hardware real (2 aparelhos no mesmo swarm).

| Métrica | Meta MVP | Medido | Notas |
|---------|----------|--------|-------|
| PSS app idle | < 120 MB | — | sem playback |
| PSS app playback HTTP | < 220 MB | — | 1 stream HLS |
| PSS app playback + P2P | < 280 MB | — | ≤3 peers |
| CPU média playback | < 35% 1 core | — | |
| Startup até 1º frame | < 5 s | — | LAN |
| Peers estáveis 10 min | sim | — | |

## Como medir

```bash
adb shell dumpsys meminfo br.com.p2pplayer
adb shell top -n 1 | grep p2pplayer
```

## Procedimento do spike WebRTC

1. Subir `server` na LAN (`ALLOW_PRIVATE_NET=true` só se origem for lab local).
2. Instalar APK em 2 TV boxes.
3. Apontar ambas para o mesmo `.m3u8` e mesmo servidor.
4. Confirmar overlay: `peers ≥ 1` e `P2P ↓` crescendo.
5. Desligar uma box → a outra continua via HTTP.
