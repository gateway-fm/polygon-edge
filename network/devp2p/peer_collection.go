package devp2p

import (
	"sync"
)

type PeerCollection struct {
	peers    map[string]*Peer
	peersMtx *sync.Mutex
}

func NewPeerCollection() *PeerCollection {
	return &PeerCollection{
		peers:    make(map[string]*Peer),
		peersMtx: &sync.Mutex{},
	}
}

func (p *PeerCollection) AddPeer(peer *Peer) {
	p.peersMtx.Lock()
	defer p.peersMtx.Unlock()
	p.peers[peer.id] = peer
}

func (p *PeerCollection) GetPeer(id string) (*Peer, bool) {
	p.peersMtx.Lock()
	defer p.peersMtx.Unlock()
	peer, ok := p.peers[id]
	return peer, ok
}

func (p *PeerCollection) DeletePeer(id string) {
	p.peersMtx.Lock()
	p.peersMtx.Unlock()
	delete(p.peers, id)
}

func (p *PeerCollection) GetAvailablePeer() *Peer {
	p.peersMtx.Lock()
	defer p.peersMtx.Unlock()

	peerCount := len(p.peers)
	if peerCount == 0 {
		return nil
	}

	var lastPeer *Peer
	for _, p := range p.peers {
		if p.CanRequest() {
			lastPeer = p
			break
		}
	}

	return lastPeer
}
