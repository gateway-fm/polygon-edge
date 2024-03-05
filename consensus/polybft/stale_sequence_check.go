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
	stopped            chan struct{}
	getHeader          func() *types.Header
}

func newStaleSequenceCheck(logger hclog.Logger, getHeader func() *types.Header, checkDuration time.Duration) *staleSequenceCheck {
	return &staleSequenceCheck{
		logger:          logger,
		currentSequence: 0,
		mtx:             &sync.Mutex{},
		checkDuration:   checkDuration,
		getHeader:       getHeader,
	}
}

func (s *staleSequenceCheck) startChecking() {
	s.stopped = make(chan struct{})
	s.sequenceShouldStop = make(chan struct{}, 1)
	s.quit = make(chan struct{})
	ticker := time.NewTicker(s.checkDuration)
	go func() {
		for {
			select {
			case <-s.quit:
				close(s.sequenceShouldStop)
				close(s.stopped)
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
	// make sure we have actually stopped and aren't still checking for staleness
	<-s.stopped
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
