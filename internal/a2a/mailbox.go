package a2a

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mailbox caps.
const (
	maxPeers    = 256
	maxMessages = 512
	// peerStale is how long a remote peer with no news stays listed.
	peerStale = 30 * time.Minute
)

// Peer is another agent this session knows of: its card, its last work, and
// when it was last heard from.
type Peer struct {
	Card   AgentCard
	Work   Work
	Seen   time.Time
	Last   string // the last broadcast's messageId
	Local  bool   // on this host's bus
	digest string // what the model was last told about it
}

// Mailbox is one session's store of what it received and sent: the peers it
// can message, and the messages that open a thread (so a reply can quote its
// question). It is safe for concurrent use.
type Mailbox struct {
	mu       sync.Mutex
	peers    map[string]*Peer
	messages map[string]Envelope
	order    []string
}

// NewMailbox returns an empty mailbox.
func NewMailbox() *Mailbox {
	return &Mailbox{peers: map[string]*Peer{}, messages: map[string]Envelope{}}
}

// Observe records an envelope: a broadcast updates its sender's entry, a
// broadcast_end removes it, and any other kind is kept by its id.
func (m *Mailbox) Observe(env Envelope, now time.Time, local bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch env.Kind {
	case KindBroadcastEnd:
		delete(m.peers, env.Agent.ID)
	case KindBroadcast:
		p := m.peers[env.Agent.ID]
		if p == nil {
			if len(m.peers) >= maxPeers {
				m.evictLocked()
			}
			p = &Peer{}
			m.peers[env.Agent.ID] = p
		}
		p.Card, p.Work, p.Seen, p.Last = env.Agent, env.Work, now, env.MessageID
		p.Local = p.Local || local
	default:
		if p := m.peers[env.Agent.ID]; p != nil {
			p.Seen = now
		}
	}
	m.keepLocked(env)
}

// Record keeps an envelope this session sent.
func (m *Mailbox) Record(env Envelope) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keepLocked(env)
}

func (m *Mailbox) keepLocked(env Envelope) {
	if _, ok := m.messages[env.MessageID]; ok {
		return
	}
	m.messages[env.MessageID] = env
	m.order = append(m.order, env.MessageID)
	if len(m.order) > maxMessages {
		delete(m.messages, m.order[0])
		m.order = m.order[1:]
	}
}

func (m *Mailbox) evictLocked() {
	var oldest string
	var at time.Time
	for id, p := range m.peers {
		if oldest == "" || p.Seen.Before(at) {
			oldest, at = id, p.Seen
		}
	}
	delete(m.peers, oldest)
}

// SetLocal replaces the same-host peers with the live cards on the bus: a
// card's process that is gone drops its peer.
func (m *Mailbox) SetLocal(cards []CardRecord, self string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := map[string]bool{}
	for _, c := range cards {
		if c.Card.ID == self {
			continue
		}
		live[c.Card.ID] = true
		p := m.peers[c.Card.ID]
		if p == nil {
			if len(m.peers) >= maxPeers {
				m.evictLocked()
			}
			p = &Peer{Card: c.Card, Seen: now}
			m.peers[c.Card.ID] = p
			if c.Last != nil && c.Last.Kind == KindBroadcast {
				p.Work, p.Last = c.Last.Work, c.Last.MessageID
			}
		}
		p.Card, p.Local = c.Card, true
	}
	for id, p := range m.peers {
		if p.Local && !live[id] {
			delete(m.peers, id)
		} else if !p.Local && now.Sub(p.Seen) > peerStale {
			delete(m.peers, id)
		}
	}
}

// Peer looks a peer up by its Agent Card id or its display id
// ("@profile·abc123", with or without the @). ok is false when no live peer
// has it, or when a display id names more than one.
func (m *Mailbox) Peer(ref string) (Peer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.peers[ref]; p != nil {
		return *p, true
	}
	want := strings.TrimPrefix(strings.TrimSpace(ref), "@")
	var found *Peer
	for _, p := range m.peers {
		if strings.TrimPrefix(p.Card.Display(), "@") == want {
			if found != nil {
				return Peer{}, false
			}
			found = p
		}
	}
	if found == nil {
		return Peer{}, false
	}
	return *found, true
}

// Peers lists the peers, most recently heard first.
func (m *Mailbox) Peers() []Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Peer, 0, len(m.peers))
	for _, p := range m.peers {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Seen.Equal(out[j].Seen) {
			return out[i].Seen.After(out[j].Seen)
		}
		return out[i].Card.ID < out[j].Card.ID
	})
	return out
}

// Message returns a kept envelope by id.
func (m *Mailbox) Message(id string) (Envelope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.messages[id]
	return e, ok
}

// ShouldTell reports whether a peer's broadcast says something the model has
// not been told: a new peer, or a change of repo, branch, PR, ticket or the
// overlap with this session's files. It records the answer, so a repeat of
// the same work is told once.
func (m *Mailbox) ShouldTell(env Envelope, overlap []string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.peers[env.Agent.ID]
	if p == nil {
		return true
	}
	d := env.Work.Repo + "\x00" + env.Work.Branch + "\x00" + env.Work.Ticket + "\x00" + strings.Join(overlap, "\x00")
	if env.Work.PR > 0 {
		d += "\x00#" + strconv.Itoa(env.Work.PR)
	}
	if p.digest == d {
		return false
	}
	p.digest = d
	return true
}
