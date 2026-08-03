# HLS P2P Streaming Android

**Experimental P2P video streaming distribution** — hybrid HLS playback (HTTP + peer-to-peer) for low-resource Android TV devices.

This repository is a **research / proof-of-concept** project. It explores how a central tracker plus WebRTC DataChannels can offload segment delivery between viewers, while keeping a simple HTTP fallback through an HLS proxy.

- **No WebView** — native Media3/ExoPlayer + WebRTC DataChannel
- **No remux/transcode** — content-addressed proxy/cache of original `.ts` segments
- **Custom protocol** — inspired by [p2p-media-loader](https://github.com/Novage/p2p-media-loader), not interoperable with it

```bash
git clone https://github.com/StgTech/p2p.git
cd p2p
```

## Disclaimer

This project is for **technical experimentation only**.

During development and testing, **publicly accessible streams found on the internet** were used as HLS sources (e.g. open `.m3u8` URLs reachable without credentials). **No illegal IPTV, pirated pay-TV, or unauthorized subscription services were used, integrated, or endorsed.**

You are responsible for complying with the terms of service and copyright laws applicable to any URL you test. Do not use this software to redistribute content you do not have the right to distribute.

## Repository layout

```
Android/      TV app (Kotlin, Media3, WebRTC)
server/       Go monolith (API, HLS proxy, WS signaling, cache)
protocol/     Message contracts and ID rules
deploy/       Docker Compose + Dockerfile
memory-bank/  Project notes
```

## Requirements

- Go 1.24+
- Android Studio / JDK 17+ (Gradle 8.13)
- Docker (optional)
- Real network for P2P (emulators often need STUN/TURN for WebRTC)

## Server

```bash
cd server
go run ./cmd/server
# listens on :8080
```

Or with Docker:

```bash
cd deploy
docker compose up --build server
```

Optional local TURN:

```bash
TURN_USER=p2p TURN_PASS=secret docker compose --profile turn up -d
```

Useful env vars: `ACCESS_SECRET`, `ALLOW_PRIVATE_NET`, `CACHE_MAX_BYTES`, `STUN_URLS`, `TURN_*`, `METERED_API_KEY`.

## Swarm dashboard

```
http://<server-host>:8080/dashboard
```

Live view of connected peers. **Video segments travel over WebRTC DataChannels** between clients; they do not pass through the signaling server.

## WebRTC: STUN and TURN

Default: **public Open Relay** (Metered) + Google STUN.

For a more reliable free tier (20 GB/month), sign up at [Open Relay](https://www.metered.ca/tools/openrelay/) and set:

```bash
METERED_API_KEY=your_key
```

Signaling stays on `ws://…/ws` (hello / peers / signal / inventory).  
Media P2P uses **DataChannel only**.

## Android app

1. Open `Android/` in Android Studio.
2. Form fields:
   - **Server URL** — default `http://192.168.2.29:8080` (use `10.0.2.2:8080` on AVD emulator)
   - **Stream URL** — public HLS `.m3u8` you are allowed to test
   - **User-Agent** — forwarded to origin via proxy
3. Overlay shows `HTTP ↓/↑`, `P2P ↓/↑`, and peer count.

ADB autoplay (testing):

```bash
adb shell am start -n br.com.p2pplayer/.MainActivity \
  --es server "http://192.168.2.29:8080" \
  --es url "https://example.com/playlist.m3u8" \
  --ez autoplay true
```

## Flow

1. App creates session: `POST /api/v1/sessions` with URL + User-Agent.
2. Server fetches playlist, picks variant, rewrites segments as `/hls/{session}/u/{urlKey}.ts`.
3. Playback: local store → **WebRTC DataChannel** → HTTP fallback.
4. Signaling on `/ws`; STUN/TURN from session `iceServers`.

Details: [protocol/PROTOCOL.md](protocol/PROTOCOL.md).

## Tests

```bash
cd server && go test ./...
cd Android && ./gradlew :app:testDebugUnitTest
```

## TV box limits (MVP)

- ≤ 3 peers, ≤ 3 parallel P2P transfers
- Short Media3 buffer (~2.5–10 s)
- ~64 MiB local segment cache
- P2P stops in `onStop` (background)

## Security

- SSRF: http(s) only; private IPs blocked by default (`ALLOW_PRIVATE_NET`)
- Credentials are not embedded in `streamKey` / path logs
- Optional `ACCESS_SECRET` via `X-Access-Secret` header or `access=` query

## Capacity planning: 3000 channels / 2000 clients

Local measurement (Docker, ~2 clients on a ~3 Mbps public HLS stream): process **~30 MB RAM**, low CPU; default **2 GB** cache fills quickly (~1600 segments). At scale, **bandwidth + disk cache** dominate, not Go CPU.

### Assumptions

| Item | Value |
|------|--------|
| Average HLS bitrate | **3–4 Mbps** (~1.2–1.5 MB / 4 s) |
| Concurrent clients | **2000** |
| Catalog | **3000** channels (not all active at once) |
| Server role | HLS proxy/cache + WS signaling (**no** media relay) |
| P2P | DataChannel; reduces HTTP on hot swarms (≥2 peers) |

Worst-case egress (no P2P):

```
egress_Gbps ≈ clients × bitrate_Mbps / 1000
           ≈ 2000 × 4 / 1000 = 8 Gbps
```

### Bandwidth (client egress)

| HTTP via proxy* | Estimated egress | Origin ingress (order of) |
|-----------------|------------------|---------------------------|
| 100% (no P2P / cold cache) | **6–8 Gbps** | ~same as egress |
| 50% (moderate P2P on hot channels) | **3–5 Gbps** | much lower |
| 20–30% (strong P2P on top N) | **1.5–3 Gbps** | seed + long tail |

\*Share of segment traffic still served over HTTP. Playlists/signaling are negligible vs `.ts`.

### Disk cache (live window ~60 s)

```
MB/active_channel ≈ (60 / 4) × 1.3 ≈ 20 MB
```

| Simultaneous active channels | Useful cache |
|------------------------------|--------------|
| 200 | ~4 GB |
| 500 | ~10–15 GB |
| 1000 | ~20–30 GB |
| Production headroom | **100–200 GB** NVMe |

Default `CACHE_MAX_BYTES=2GiB` is lab-only — use ≥100 GB in production.

### RAM / CPU

| Resource | Estimate @ 2000 clients |
|----------|-------------------------|
| RAM + `.ts` buffers | **4–8 GB** |
| CPU | **4–8 vCPU** |
| WS signaling | cheap vs HLS |

### Recommended server

| Profile | Spec | Link | Cache |
|---------|------|------|-------|
| Pilot (≤500 clients) | 4 vCPU / 8 GB | 1–2 Gbps | 20–50 GB |
| **Production 2000 / 3000** | **8 vCPU / 16 GB** | **5–10 Gbps** | **100–200 GB** NVMe |
| Conservative (weak P2P) | 16 vCPU / 32 GB | 10 Gbps+ or 2–3 nodes | 200–500 GB |

Dedicated 10 Gbps (Hetzner/OVH) is often cheaper than hyperscaler **egress**.

TURN: free Open Relay does **not** scale to thousands of clients — use paid Metered or self-hosted **coturn**.

### Pre-production checklist

1. `CACHE_MAX_BYTES` ≥ 100 GB on SSD/NVMe  
2. Raise `MaxIdleConns` / origin HTTP pool (32 is too low for thousands of clients)  
3. Stream segments with `io.Copy` instead of loading full `.ts` into RAM  
4. Rate-limit + `ACCESS_SECRET`  
5. Metrics: egress Gbps, cache hit, sessions, swarm size per `streamKey`  
6. 2+ replicas behind a load balancer if one NIC cannot cover bandwidth  

### Quick example

```
clients=2000  bitrate=4Mbps  p2p_offload=50%
egress ≈ 2000×4×0.50 = 4000 Mbps ≈ 4 Gbps  → 5–10 Gbps link
active_channels≈400 → cache ≈ 400×20MB ≈ 8 GB (+ headroom → 100 GB)
machine: 8 vCPU / 16 GB RAM / NVMe
```
