package polybft

import (
	"errors"
	"fmt"
	"sync"

	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/validator"
	"github.com/0xPolygon/polygon-edge/types"
)

type validatorSnapshot struct {
	Epoch            uint64               `json:"epoch"`
	EpochEndingBlock uint64               `json:"epochEndingBlock"`
	Snapshot         validator.AccountSet `json:"snapshot"`
}

func (vs *validatorSnapshot) copy() *validatorSnapshot {
	copiedAccountSet := vs.Snapshot.Copy()

	return &validatorSnapshot{
		Epoch:            vs.Epoch,
		EpochEndingBlock: vs.EpochEndingBlock,
		Snapshot:         copiedAccountSet,
	}
}

type validatorsSnapshotCache struct {
	snapshots  map[uint64]*validatorSnapshot
	state      *State
	blockchain blockchainBackend
	lock       sync.Mutex
	logger     hclog.Logger
	forkBlock  uint64
}

// newValidatorsSnapshotCache initializes a new instance of validatorsSnapshotCache
func newValidatorsSnapshotCache(
	logger hclog.Logger, state *State, blockchain blockchainBackend,
	forkBlock uint64,
) *validatorsSnapshotCache {
	return &validatorsSnapshotCache{
		snapshots:  map[uint64]*validatorSnapshot{},
		state:      state,
		blockchain: blockchain,
		logger:     logger.Named("validators_snapshot"),
		forkBlock:  forkBlock,
	}
}

// GetSnapshot tries to retrieve the most recent cached snapshot (if any) and
// applies pending validator set deltas to it.
// Otherwise, it builds a snapshot from scratch and applies pending validator set deltas.
func (v *validatorsSnapshotCache) GetSnapshot(
	blockNumber uint64,
	parents []*types.Header,
	forkBlock uint64,
) (validator.AccountSet, error) {
	v.lock.Lock()
	defer v.lock.Unlock()

	_, extra, err := getBlockData(blockNumber, v.blockchain)
	if err != nil {
		return nil, err
	}

	epochEndingBlock, err := isEpochEndingBlock(blockNumber, extra, v.blockchain)
	if err != nil && !errors.Is(err, blockchain.ErrNoBlock) {
		// if there is no block after given block, we assume its not epoch ending block
		// but, it's a regular use case, and we should not stop the snapshot calculation
		// because there are cases we need the snapshot for the latest block in chain
		return nil, err
	}
	if blockNumber == forkBlock && forkBlock > 0 {
		epochEndingBlock = false
	}

	epochToGetSnapshot := extra.Checkpoint.EpochNumber
	if !epochEndingBlock {
		epochToGetSnapshot--
	}

	v.logger.Info("vals: Retrieving snapshot started...", "block", blockNumber, "epoch", epochToGetSnapshot, "epoch-ending", epochEndingBlock)

	latestValidatorSnapshot, err := v.getLastSnapshot(epochToGetSnapshot)
	if err != nil {
		return nil, err
	}

	if latestValidatorSnapshot != nil && latestValidatorSnapshot.Epoch == epochToGetSnapshot {
		v.logger.Info("vals: latest validator snapshot matches epoch", "epoch", epochToGetSnapshot)
		// we have snapshot for required block (epoch) in cache
		return latestValidatorSnapshot.Snapshot, nil
	}

	if latestValidatorSnapshot == nil {
		// Haven't managed to retrieve snapshot for any epoch from the cache.
		// Build snapshot from the scratch, by applying delta from the genesis block.
		genesisBlockSnapshot, err := v.computeSnapshot(nil, forkBlock, parents)
		if err != nil {
			return nil, fmt.Errorf("failed to compute snapshot for epoch 0: %w", err)
		}

		err = v.storeSnapshot(genesisBlockSnapshot)
		if err != nil {
			return nil, fmt.Errorf("failed to store validators snapshot for epoch 0: %w", err)
		}

		latestValidatorSnapshot = genesisBlockSnapshot

		v.logger.Info("vals: built validators snapshot for genesis block")
	}

	deltasCount := 0

	v.logger.Info("vals: Applying deltas started...",
		"LatestSnapshotEpoch", latestValidatorSnapshot.Epoch,
		"epochToGetSnapshot", epochToGetSnapshot)

	// Create the snapshot for the desired block (epoch) by incrementally applying deltas to the latest stored snapshot
	for latestValidatorSnapshot.Epoch < epochToGetSnapshot {
		nextEpochEndBlockNumber, err := v.getNextEpochEndingBlock(latestValidatorSnapshot.EpochEndingBlock)
		if err != nil {
			return nil, fmt.Errorf("failed to get the epoch ending block for epoch: %d. Error: %w",
				latestValidatorSnapshot.Epoch+1, err)
		}

		v.logger.Info("vals: Applying delta", "epochEndBlock", nextEpochEndBlockNumber)

		intermediateSnapshot, err := v.computeSnapshot(latestValidatorSnapshot, nextEpochEndBlockNumber, parents)
		if err != nil {
			return nil, fmt.Errorf("failed to compute snapshot for epoch %d: %w", latestValidatorSnapshot.Epoch+1, err)
		}

		latestValidatorSnapshot = intermediateSnapshot
		if err = v.storeSnapshot(latestValidatorSnapshot); err != nil {
			return nil, fmt.Errorf("failed to store validators snapshot for epoch %d: %w", latestValidatorSnapshot.Epoch, err)
		}

		deltasCount++
	}

	v.logger.Info(
		fmt.Sprintf("vals: Applied %d delta(s) to the validators snapshot", deltasCount),
		"Epoch", latestValidatorSnapshot.Epoch,
		"snapshot", latestValidatorSnapshot.Snapshot,
	)

	if err := v.cleanup(); err != nil {
		// error on cleanup should not block or fail any action
		v.logger.Error("could not clean validator snapshots from cache and db", "err", err)
	}

	return latestValidatorSnapshot.Snapshot, nil
}

// computeSnapshot gets desired block header by block number, extracts its extra and applies given delta to the snapshot
func (v *validatorsSnapshotCache) computeSnapshot(
	existingSnapshot *validatorSnapshot,
	blockNumber uint64,
	parents []*types.Header,
) (*validatorSnapshot, error) {
	var header *types.Header

	v.logger.Trace("Compute snapshot started...", "BlockNumber", blockNumber)

	if len(parents) > 0 {
		for i := len(parents) - 1; i >= 0; i-- {
			parentHeader := parents[i]
			if parentHeader.Number == blockNumber {
				v.logger.Trace("Compute snapshot. Found header in parents", "Header", parentHeader.Number)
				header = parentHeader

				break
			}
		}
	}

	if header == nil {
		var ok bool

		header, ok = v.blockchain.GetHeaderByNumber(blockNumber)
		if !ok {
			return nil, fmt.Errorf("unknown block. Block number=%v", blockNumber)
		}
	}

	extra, err := GetIbftExtra(header.ExtraData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode extra from the block#%d: %w", header.Number, err)
	}

	var (
		snapshot      validator.AccountSet
		snapshotEpoch uint64
	)

	if existingSnapshot == nil {
		snapshot = validator.AccountSet{}
	} else {
		snapshot = existingSnapshot.Snapshot
		snapshotEpoch = existingSnapshot.Epoch + 1
	}

	snapshot, err = snapshot.ApplyDelta(extra.Validators)
	if err != nil {
		return nil, fmt.Errorf("failed to apply delta to the validators snapshot, block#%d: %w", header.Number, err)
	}

	v.logger.Debug("Computed snapshot",
		"blockNumber", blockNumber,
		"snapshot", snapshot.String(),
		"delta", extra.Validators)

	return &validatorSnapshot{
		Epoch:            snapshotEpoch,
		EpochEndingBlock: blockNumber,
		Snapshot:         snapshot,
	}, nil
}

// storeSnapshot stores given snapshot to the in-memory cache and database
func (v *validatorsSnapshotCache) storeSnapshot(snapshot *validatorSnapshot) error {
	copySnap := snapshot.copy()
	v.snapshots[copySnap.Epoch] = copySnap

	if err := v.state.EpochStore.insertValidatorSnapshot(copySnap); err != nil {
		return fmt.Errorf("failed to insert validator snapshot for epoch %d to the database: %w", copySnap.Epoch, err)
	}

	v.logger.Trace("Store snapshot", "Snapshots", v.snapshots)

	return nil
}

// Cleanup cleans the validators cache in memory and db
func (v *validatorsSnapshotCache) cleanup() error {
	if len(v.snapshots) >= validatorSnapshotLimit {
		latestEpoch := uint64(0)

		for e := range v.snapshots {
			if e > latestEpoch {
				latestEpoch = e
			}
		}

		startEpoch := latestEpoch
		cache := make(map[uint64]*validatorSnapshot, numberOfSnapshotsToLeaveInMemory)

		for i := 0; i < numberOfSnapshotsToLeaveInMemory; i++ {
			if snapshot, exists := v.snapshots[startEpoch]; exists {
				cache[startEpoch] = snapshot
			}

			startEpoch--
		}

		v.snapshots = cache

		v.logger.Info("validators snapshots cleaned up", "latest-epoch", latestEpoch)

		removed until we can understand why this drops the db every time
		return v.state.EpochStore.cleanValidatorSnapshotsFromDB(latestEpoch)
	}

	return nil
}

// getLastSnapshot gets the latest snapshot from cache or db based on the passed epoch number
func (v *validatorsSnapshotCache) getLastSnapshot(epoch uint64) (*validatorSnapshot, error) {
	snapshot := v.snapshots[epoch]
	if snapshot != nil {
		v.logger.Debug("vals: found cached snapshot immediately", "epoch", epoch)
		return snapshot, nil
	}

	// now try the DB to see if the snapshot is there
	snapshot, err := v.state.EpochStore.getValidatorSnapshot(epoch)
	if err != nil {
		// log the error and move on
		v.logger.Error("vals: failed to get snapshot from db", "epoch", epoch, "err", err)
	}
	if snapshot != nil {
		v.logger.Info("vals: found snapshot in db immediately - adding to cache", "epoch", epoch)
		// store this epoch in the cache for later
		v.snapshots[epoch] = snapshot.copy()
		return snapshot, nil
	}

	// if we do not have a snapshot in memory for given epoch, we will get the latest one we have
	for ; epoch >= 0; epoch-- {
		snapshot = v.snapshots[epoch]
		if snapshot != nil {
			v.logger.Debug("vals: found snapshot in memory cache", "epoch", epoch)
			break
		}

		// check the db also for this snapshot
		snapshot, err = v.state.EpochStore.getValidatorSnapshot(epoch)
		if err != nil {
			// log the error and move on
			v.logger.Error("vals: failed to get snapshot from db", "epoch", epoch, "err", err)
		}
		if snapshot != nil {
			v.logger.Info("vals: found snapshot in db - adding to cache", "epoch", epoch)
			// store this epoch in the cache for later
			v.snapshots[epoch] = snapshot.copy()
			break
		}

		if epoch == 0 { // prevent uint64 underflow
			break
		}
	}

	dbSnapshot, err := v.state.EpochStore.getLastSnapshot()
	if err != nil {
		return nil, err
	}

	if dbSnapshot != nil {
		// if we do not have any snapshot in memory, or db snapshot is newer than the one in memory
		// return the one from db
		if snapshot == nil || dbSnapshot.Epoch > snapshot.Epoch {
			snapshot = dbSnapshot
			// save it in cache as well, since it doesn't exist
			v.snapshots[dbSnapshot.Epoch] = dbSnapshot.copy()
		}
	}

	return snapshot, nil
}

// getNextEpochEndingBlock gets the epoch ending block of a newer epoch
// It start checking the blocks from the provided epoch ending block of the previous epoch
func (v *validatorsSnapshotCache) getNextEpochEndingBlock(latestEpochEndingBlock uint64) (uint64, error) {
	blockNumber := latestEpochEndingBlock + 1 // get next block

	_, extra, err := getBlockData(blockNumber, v.blockchain)
	if err != nil {
		return 0, err
	}

	startEpoch := extra.Checkpoint.EpochNumber
	epoch := startEpoch

	for startEpoch == epoch {
		blockNumber++

		_, extra, err = getBlockData(blockNumber, v.blockchain)
		if err != nil {
			if errors.Is(err, blockchain.ErrNoBlock) {
				return blockNumber - 1, nil
			}

			return 0, err
		}

		epoch = extra.Checkpoint.EpochNumber
	}

	return blockNumber - 1, nil
}
