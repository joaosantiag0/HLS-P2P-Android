package signaling

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type Hub struct {
	mu     sync.RWMutex
	swarms map[string]map[string]*Peer // streamKey -> peerId -> peer
}

type Peer struct {
	ID        string
	StreamKey string
	SessionID string
	conn      *websocket.Conn
	send      chan []byte
	hub       *Hub
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

func NewHub() *Hub {
	return &Hub{swarms: make(map[string]map[string]*Peer)}
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
		case "signal", "p2p-want", "p2p-chunk", "p2p-done", "p2p-nack":
			msg.From = p.ID
			p.hub.forward(p.StreamKey, msg.To, msg)
		case "inventory":
			msg.From = p.ID
			p.hub.broadcast(p.StreamKey, p.ID, msg)
		case "ping":
			p.sendJSON(envelope{Type: "pong"})
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
}

func (h *Hub) forward(streamKey, to string, msg envelope) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	swarm := h.swarms[streamKey]
	if swarm == nil {
		return
	}
	peer, ok := swarm[to]
	if !ok {
		return
	}
	peer.sendJSON(msg)
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
