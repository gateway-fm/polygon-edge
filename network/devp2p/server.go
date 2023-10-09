package devp2p

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/blockchain"
)

type Config struct {
	ListenAddress string
	StaticPeers   []string
	Blockchain    *blockchain.Blockchain
}

type Server struct {
	logger         hclog.Logger
	config         *Config
	p2p            *p2p.Server
	peerCollection *PeerCollection
}

func NewServer(logger hclog.Logger, cfg *Config, bc *blockchain.Blockchain) (*Server, error) {
	logger = logger.Named("network-devp2p")

	srv := &Server{
		config:         cfg,
		logger:         logger,
		peerCollection: NewPeerCollection(),
	}

	// set up the protocols
	protocols := make([]p2p.Protocol, len(ProtocolVersions))
	for i, version := range ProtocolVersions {
		v := version
		protocols[i] = p2p.Protocol{
			Name:    ProtocolName,
			Version: v,
			Length:  protocolLengths[v],
			Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
				peerId := p.ID().String()

				pr := NewPeer(peerId, v, p, rw, big.NewInt(0))

				srv.peerCollection.AddPeer(pr)

				logger.Info("added peer", "enode", p.Info().Enode, "peerId", peerId)

				err := srv.RunPeer(pr, bc)

				return err

			},
			NodeInfo: func() interface{} {
				info := &NodeInfo{
					Network:    uint64(cfg.Blockchain.Config().ChainID),
					Difficulty: cfg.Blockchain.CurrentTD(),
					Genesis:    cfg.Blockchain.Genesis(),
					Config:     cfg.Blockchain.Config(),
					Head:       cfg.Blockchain.Header().Hash,
				}
				return info
			},
			PeerInfo: func(id enode.ID) interface{} {
				if p, ok := srv.peerCollection.GetPeer(id.String()); ok {
					return peerInfo{p.Version()}
				}

				return nil
			},
		}
	}

	// todo: get the key from the secrets store
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}

	p2pcfg := p2p.Config{
		PrivateKey:      key,
		Name:            "server",
		Protocols:       protocols,
		ListenAddr:      cfg.ListenAddress,
		EnableMsgEvents: true,
		MaxPeers:        100,
		//Logger:          log,
	}

	srv.p2p = &p2p.Server{
		Config: p2pcfg,
	}

	return srv, nil
}

// Start starts the server if there are any static peers defined in the config otherwise it will just return a
// nil error
func (s *Server) Start() error {
	if s.p2p == nil {
		return fmt.Errorf("you need to define the p2p server first")
	}

	// as we're not using discovery on devp2p, only static peers, if there are none then just return
	if len(s.config.StaticPeers) == 0 {
		return nil
	}

	err := s.p2p.Start()
	if err != nil {
		return err
	}

	// only handling static peers - no discovery - so add them here
	for _, peer := range s.config.StaticPeers {
		n, err := enode.ParseV4(peer)
		if err != nil {
			return err
		}
		s.p2p.AddPeer(n)
	}

	// watch for global events on the p2p network from any peers
	go func() {
		events := make(chan *p2p.PeerEvent)
		eventSub := s.p2p.SubscribeEvents(events)

		for {
			select {
			case e := <-events:
				s.logger.Debug("received peer event", "type", e.Type)
				if e.Type == p2p.PeerEventTypeDrop {
					func() {
						id := e.Peer.String()
						if _, ok := s.peerCollection.GetPeer(id); !ok {
							s.logger.Info("Peer drop received for unknown peer", "peerId", id)
							return
						}
						s.peerCollection.DeletePeer(id)
						s.logger.Info("Peer dropped", "peerId", id)
					}()
				}
			case <-eventSub.Err():
				s.logger.Error("event sub error")
			}
		}
	}()

	s.logger.Info("devp2p running", "address", s.p2p.NodeInfo().Enode)

	return nil
}

func (s *Server) Stop() {
	s.p2p.Stop()
}

func (s *Server) RunPeer(pr *Peer, bc *blockchain.Blockchain) error {
	// perform the handshake - read their status message and send our own
	noErrorForkIdFilter := func(id forkid.ID) error { return nil }
	err := pr.Handshake(
		uint64(s.config.Blockchain.Config().ChainID),
		s.config.Blockchain.CurrentTD(),
		s.config.Blockchain.Header().Hash,
		s.config.Blockchain.Genesis(),
		forkid.ID{},
		noErrorForkIdFilter,
	)
	if err != nil {
		s.logger.Error("error performing peer handshake", "err", err, "peerId", pr.id)
		return err
	}

	bc.NewBlockNumberAnnounced(pr.highestBlock)

	// now start listening for messages on the peer
	for {
		if err := s.handleMessage(pr); err != nil {
			s.logger.Debug("Error handling peer message", "err", err)
			return err
		}
	}
}

func (s *Server) handleMessage(pr *Peer) error {
	msg, err := pr.readWriter.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Size > maxMessageSize {
		return fmt.Errorf("%w: %v > %v", errMsgTooLarge, msg.Size, maxMessageSize)
	}
	defer msg.Discard()

	s.logger.Debug("p2p msg received", "code", msg.Code)

	handlers := eth66
	if pr.version == 67 {
		handlers = eth67
	}
	if pr.version == 68 {
		handlers = eth68
	}

	handler, ok := handlers[msg.Code]
	if !ok {
		s.logger.Error("No handler registered for p2p message", "code", msg.Code)
		return nil
	}

	return handler(s.config.Blockchain, msg, pr)
}

func (s *Server) GetAvailablePeer() *Peer {
	return s.peerCollection.GetAvailablePeer()
}
