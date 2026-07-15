package signaling

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Hub struct {
	mu           sync.RWMutex
	swarms       map[string]map[string]*Peer // streamKey -> peerId -> peer
	stats        *Stats
	dash         map[*dashboardClient]struct{}
	dashOnce     sync.Once
	lastDashPush atomic.Int64
}

type Peer struct {
	ID        string
	StreamKey string
	SessionID string
	conn      *websocket.Conn
	send      chan []byte
	hub       *Hub
}

type dashboardClient struct {
	conn *websocket.Conn
	send chan []byte
}

func NewHub() *Hub {
	return &Hub{
		swarms: make(map[string]map[string]*Peer),
		stats:  NewStats(),
		dash:   make(map[*dashboardClient]struct{}),
	}
}

type envelope struct {
	Type       string          `json:"type"`
	PeerID     string          `json:"peerId,omitempty"`
	StreamKey  string          `json:"streamKey,omitempty"`
	SessionID  string          `json:"sessionId,omitempty"`
	To         string          `json:"to,omitempty"`
	From       string          `json:"from,omitempty"`
	SdpType    string          `json:"sdpType,omitempty"`
	SDP        string          `json:"sdp,omitempty"`
	Candidate  json.RawMessage `json:"candidate,omitempty"`
	Peers      []peerInfo      `json:"peers,omitempty"`
	SegmentIDs []string        `json:"segmentIds,omitempty"`
	ID         string          `json:"id,omitempty"`
	Offset     int             `json:"offset"`
	Total      int             `json:"total,omitempty"`
	Data       string          `json:"data,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

type peerInfo struct {
	PeerID string `json:"peerId"`
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	p := &Peer{
		conn: conn,
		send: make(chan []byte, 1024),
		hub:  h,
	}
	conn.SetReadLimit(8 << 20) // 8 MiB — chunks de relay
	go p.writePump()
	p.readPump()
}

func (p *Peer) readPump() {
	defer func() {
		p.hub.unregister(p)
		_ = p.conn.Close()
	}()
	_ = p.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	p.conn.SetPongHandler(func(string) error {
		_ = p.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		_, data, err := p.conn.ReadMessage()
		if err != nil {
			return
		}
		var msg envelope
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "hello":
			p.ID = msg.PeerID
			p.StreamKey = msg.StreamKey
			p.SessionID = msg.SessionID
			if p.ID == "" || p.StreamKey == "" {
				continue
			}
			p.hub.register(p)
		case "signal":
			msg.From = p.ID
			p.hub.forward(p.StreamKey, msg.To, msg)
		case "inventory":
			msg.From = p.ID
			p.hub.broadcast(p.StreamKey, p.ID, msg)
		case "ping":
			p.sendJSON(envelope{Type: "pong"})
		// p2p-want/chunk/done removidos: segmentos só via WebRTC DataChannel (P2P real).
		}
	}
}

func (p *Peer) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		_ = p.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-p.send:
			_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = p.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := p.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (p *Peer) sendJSON(v envelope) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case p.send <- b:
	default:
		// Evita dropar chunks de relay silenciosamente: bloqueia um pouco.
		select {
		case p.send <- b:
		case <-time.After(2 * time.Second):
			log.Printf("peer %s send buffer full, dropping %s", shortID(p.ID), v.Type)
		}
	}
}

func (h *Hub) register(p *Peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	swarm, ok := h.swarms[p.StreamKey]
	if !ok {
		swarm = make(map[string]*Peer)
		h.swarms[p.StreamKey] = swarm
	}
	// replace existing
	if old, exists := swarm[p.ID]; exists {
		close(old.send)
		_ = old.conn.Close()
	}
	swarm[p.ID] = p
	log.Printf("swarm join streamKey=%s peer=%s size=%d", truncateKey(p.StreamKey), shortID(p.ID), len(swarm))

	others := make([]peerInfo, 0, len(swarm)-1)
	for id := range swarm {
		if id != p.ID {
			others = append(others, peerInfo{PeerID: id})
		}
	}
	p.sendJSON(envelope{Type: "peers", Peers: others})
	for id, peer := range swarm {
		if id == p.ID {
			continue
		}
		peer.sendJSON(envelope{Type: "peer-joined", PeerID: p.ID})
	}
	go h.broadcastDashboard()
}

func (h *Hub) unregister(p *Peer) {
	if p.ID == "" || p.StreamKey == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	swarm, ok := h.swarms[p.StreamKey]
	if !ok {
		return
	}
	if cur, ok := swarm[p.ID]; ok && cur == p {
		delete(swarm, p.ID)
		close(p.send)
		for _, peer := range swarm {
			peer.sendJSON(envelope{Type: "peer-left", PeerID: p.ID})
		}
	}
	if len(swarm) == 0 {
		delete(h.swarms, p.StreamKey)
	}
	log.Printf("swarm leave streamKey=%s peer=%s size=%d", truncateKey(p.StreamKey), shortID(p.ID), len(swarm))
	go h.broadcastDashboard()
}

func (h *Hub) forward(streamKey, to string, msg envelope) {
	h.mu.RLock()
	swarm := h.swarms[streamKey]
	var peer *Peer
	if swarm != nil {
		peer = swarm[to]
	}
	h.mu.RUnlock()
	if peer == nil {
		return
	}
	switch msg.Type {
	case "p2p-chunk":
		h.stats.RecordChunk(streamKey, msg.From, to, msg.ID, msg.Data)
	case "p2p-done":
		h.stats.RecordTransferDone(streamKey, msg.From, to, msg.ID)
	}
	peer.sendJSON(msg)
	if msg.Type == "p2p-chunk" || msg.Type == "p2p-done" {
		// Throttle: no máximo ~5 Hz durante chunks
		now := time.Now().UnixMilli()
		last := h.lastDashPush.Load()
		if msg.Type == "p2p-done" || now-last >= 200 {
			if h.lastDashPush.CompareAndSwap(last, now) {
				h.broadcastDashboard()
			}
		}
	}
}

func (h *Hub) ListPeers() []PeerNode {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]PeerNode, 0)
	for sk, swarm := range h.swarms {
		for id, p := range swarm {
			out = append(out, PeerNode{
				ID:        id,
				ShortID:   shortID(id),
				StreamKey: sk,
				SessionID: p.SessionID,
			})
		}
	}
	return out
}

func (h *Hub) Snapshot() SwarmSnapshot {
	peers := h.ListPeers()
	h.mu.RLock()
	n := len(h.swarms)
	h.mu.RUnlock()
	return h.stats.Snapshot(peers, n)
}

func (h *Hub) ServeDashboardWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &dashboardClient{
		conn: conn,
		send: make(chan []byte, 8),
	}
	h.mu.Lock()
	h.dash[c] = struct{}{}
	h.mu.Unlock()
	h.dashOnce.Do(func() {
		go func() {
			t := time.NewTicker(2 * time.Second)
			for range t.C {
				h.broadcastDashboard()
			}
		}()
	})

	if b, err := json.Marshal(h.Snapshot()); err == nil {
		select {
		case c.send <- b:
		default:
		}
	}

	go func() {
		for msg := range c.send {
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				break
			}
		}
	}()

	defer func() {
		h.mu.Lock()
		delete(h.dash, c)
		h.mu.Unlock()
		close(c.send)
		_ = conn.Close()
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *Hub) broadcastDashboard() {
	b, err := json.Marshal(h.Snapshot())
	if err != nil {
		return
	}
	h.mu.RLock()
	clients := make([]*dashboardClient, 0, len(h.dash))
	for c := range h.dash {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		select {
		case c.send <- b:
		default:
			// cliente lento — drop
		}
	}
}

func (h *Hub) broadcast(streamKey, from string, msg envelope) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	swarm := h.swarms[streamKey]
	for id, peer := range swarm {
		if id == from {
			continue
		}
		peer.sendJSON(msg)
	}
}

func (h *Hub) SwarmSize(streamKey string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.swarms[streamKey])
}

func truncateKey(k string) string {
	if len(k) <= 48 {
		return k
	}
	return k[:20] + "…" + k[len(k)-12:]
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
