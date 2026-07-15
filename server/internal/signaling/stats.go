package signaling

import (
	"sort"
	"sync"
	"time"
)

// FlowEdge agrega bytes/transfers de um peer → outro (relay WS visível no servidor).
type FlowEdge struct {
	From        string `json:"from"`
	To          string `json:"to"`
	StreamKey   string `json:"streamKey"`
	Bytes       int64  `json:"bytes"`
	Transfers   int64  `json:"transfers"`
	LastSeg     string `json:"lastSegment,omitempty"`
	UpdatedAt   int64  `json:"updatedAt"`
	BytesPerSec int64  `json:"bytesPerSec"`
}

type PeerNode struct {
	ID        string `json:"id"`
	ShortID   string `json:"shortId"`
	StreamKey string `json:"streamKey"`
	SessionID string `json:"sessionId,omitempty"`
}

type SwarmSnapshot struct {
	Time       int64      `json:"time"`
	Peers      []PeerNode `json:"peers"`
	Edges      []FlowEdge `json:"edges"`
	Note       string     `json:"note"`
	SwarmCount int        `json:"swarmCount"`
}

type flowKey struct {
	from, to, stream string
}

type edgeAccum struct {
	FlowEdge
	windowBytes int64
	windowStart time.Time
}

type Stats struct {
	mu    sync.Mutex
	edges map[flowKey]*edgeAccum
}

func NewStats() *Stats {
	return &Stats{edges: make(map[flowKey]*edgeAccum)}
}

func (s *Stats) RecordChunk(streamKey, from, to, segmentID, dataB64 string) {
	if from == "" || to == "" {
		return
	}
	n := int64(len(dataB64) * 3 / 4)
	s.mu.Lock()
	defer s.mu.Unlock()
	k := flowKey{from: from, to: to, stream: streamKey}
	e := s.edges[k]
	if e == nil {
		e = &edgeAccum{
			FlowEdge: FlowEdge{
				From:      from,
				To:        to,
				StreamKey: streamKey,
			},
			windowStart: time.Now(),
		}
		s.edges[k] = e
	}
	now := time.Now()
	if now.Sub(e.windowStart) >= time.Second {
		e.BytesPerSec = e.windowBytes
		e.windowBytes = 0
		e.windowStart = now
	}
	e.Bytes += n
	e.windowBytes += n
	e.LastSeg = shortID(segmentID)
	e.UpdatedAt = now.UnixMilli()
}

func (s *Stats) RecordTransferDone(streamKey, from, to, segmentID string) {
	if from == "" || to == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := flowKey{from: from, to: to, stream: streamKey}
	e := s.edges[k]
	if e == nil {
		e = &edgeAccum{
			FlowEdge: FlowEdge{
				From:      from,
				To:        to,
				StreamKey: streamKey,
			},
			windowStart: time.Now(),
		}
		s.edges[k] = e
	}
	e.Transfers++
	e.LastSeg = shortID(segmentID)
	e.UpdatedAt = time.Now().UnixMilli()
}

func (s *Stats) Snapshot(peers []PeerNode, swarmCount int) SwarmSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	edges := make([]FlowEdge, 0, len(s.edges))
	cutoff := time.Now().Add(-10 * time.Minute).UnixMilli()
	for k, e := range s.edges {
		if e.UpdatedAt < cutoff && e.BytesPerSec == 0 {
			delete(s.edges, k)
			continue
		}
		edges = append(edges, e.FlowEdge)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].BytesPerSec != edges[j].BytesPerSec {
			return edges[i].BytesPerSec > edges[j].BytesPerSec
		}
		return edges[i].Bytes > edges[j].Bytes
	})
	return SwarmSnapshot{
		Time:       time.Now().UnixMilli(),
		Peers:      peers,
		Edges:      edges,
		SwarmCount: swarmCount,
      Note:       "Peers na sinalização. P2P de segmentos = WebRTC DataChannel (STUN/TURN). Bytes do DC não passam por este servidor.",
	}
}
