package polybft

import (
	"testing"
	"github.com/0xPolygon/polygon-edge/types"
	"time"
	"github.com/hashicorp/go-hclog"
)

type mockHeaderGetter struct {
	count        int
	target       int
	headerNumber uint64
}

func (mh *mockHeaderGetter) GetHeader() *types.Header {
	mh.count++
	if mh.count >= mh.target {
		return &types.Header{Number: mh.headerNumber}
	} else {
		return &types.Header{Number: uint64(0)}
	}
}

func Test_sequenceStaleCheck(t *testing.T) {
	mh := mockHeaderGetter{target: 3, headerNumber: 10}
	staleCheck := newStaleSequenceCheck(hclog.L(), mh.GetHeader, 1*time.Millisecond)
	staleCheck.setSequence(9)

	timeout := time.NewTicker(10 * time.Millisecond)
	state := make(chan int)
	staleCheck.startChecking()
	go func() {
		select {
		case <-timeout.C:
			state <- 1
			return
		case <-staleCheck.sequenceShouldStop:
			state <- 2
			return
		}
	}()

	result := <-state

	staleCheck.stopChecking()

	if result == 1 {
		t.Fatal("test timed out waiting for condition")
	}
}

// start a longer running check and stop it mid-flow
func Test_sequenceStaleCheck_QuitWhilstChecking(t *testing.T) {
	mh := mockHeaderGetter{target: 5, headerNumber: 10}
	staleCheck := newStaleSequenceCheck(hclog.L(), mh.GetHeader, 1*time.Millisecond)
	staleCheck.setSequence(9)

	timeout := time.NewTicker(10 * time.Millisecond)
	state := make(chan int)
	staleCheck.startChecking()
	go func() {
		select {
		case <-timeout.C:
			state <- 1
			return
		case <-staleCheck.sequenceShouldStop:
			state <- 2
			return
		}
	}()

	timer := time.NewTimer(2 * time.Millisecond)
	<-timer.C
	staleCheck.stopChecking()

	result := <-state

	if result == 1 {
		t.Fatal("test timed out waiting for condition")
	}
}

// a bug highlighted an issue with calling close on a closed channel so this test
// proves off multiple calls to start and stop.
func Test_sequenceStaleCheck_ChannelCheck(t *testing.T) {
	mh := mockHeaderGetter{target: 5, headerNumber: 10}
	staleCheck := newStaleSequenceCheck(hclog.L(), mh.GetHeader, 1*time.Millisecond)
	staleCheck.setSequence(9)

	for i := 0; i < 1000; i++ {
		staleCheck.startChecking()
		staleCheck.stopChecking()
	}
}
