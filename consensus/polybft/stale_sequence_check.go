package polybft

import (
	"sync"
	"github.com/0xPolygon/polygon-edge/types"
	"time"
	"github.com/hashicorp/go-hclog"
)

type staleSequenceCheck struct {
	logger             hclog.Logger
	currentSequence    uint64
	mtx                *sync.Mutex
	checkDuration      time.Duration
	sequenceShouldStop chan struct{}
	quit               chan struct{}
	getHeader          func() *types.Header
}

func newStaleSequenceCheck(logger hclog.Logger, getHeader func() *types.Header, checkDuration time.Duration) *staleSequenceCheck {
	return &staleSequenceCheck{
		logger:             logger,
		currentSequence:    0,
		mtx:                &sync.Mutex{},
		checkDuration:      checkDuration,
		sequenceShouldStop: make(chan struct{}, 1),
		quit:               make(chan struct{}),
		getHeader:          getHeader,
	}
}

func (s *staleSequenceCheck) startChecking() {
	ticker := time.NewTicker(s.checkDuration)
	go func() {
		for {
			select {
			case <-s.quit:
				close(s.sequenceShouldStop)
				ticker.Stop()
				return
			case <-ticker.C:
				s.checkForStaleness()
			}
		}
	}()
}

func (s *staleSequenceCheck) stopChecking() {
	close(s.quit)
}

func (s *staleSequenceCheck) setSequence(sequence uint64) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.currentSequence = sequence
}

func (s *staleSequenceCheck) checkForStaleness() {
	s.logger.Info("[staleSequenceCheck] checking for stale sequence")
	header := s.getHeader()
	s.chainHeightUpdated(header.Number)
}

func (s *staleSequenceCheck) chainHeightUpdated(height uint64) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.currentSequence == 0 {
		return
	}
	if height >= s.currentSequence {
		s.logger.Info("[staleSequenceCheck] stale sequence detected", "height", height, "currentSequence", s.currentSequence)
		s.sequenceShouldStop <- struct{}{}
	}
}
