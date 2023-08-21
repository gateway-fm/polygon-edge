package server

import (
	"fmt"
	"math/big"
	"net/http"
	"path/filepath"

	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/leveldb"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/memory"
	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/consensus/polybft"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/contractsapi"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/validator"
	"github.com/0xPolygon/polygon-edge/contracts"
	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
)

var dirPaths = []string{
	"blockchain",
	"trie",
}

// Manager is the main server manager - designed to exist once per process and will
// use the fork information from the genesis.json file to load to appropriate consensus engine
// for each fork and manage the transition between them by spinning up and down fresh Server instances
type Manager struct {
	Config               *Config
	active               *Server
	logger               hclog.Logger
	prometheusServer     *http.Server
	secretsManager       secrets.SecretsManager
	stateStorage         itrie.Storage
	state                *itrie.State
	db                   storage.Storage
	originalEngineConfig map[string]interface{}
	nextFork             *uint64
	currentHeader        *types.Header
	storeContainer       *jsonrpc.StoreContainer
}

func NewManager(cfg *Config) (*Manager, error) {
	m := &Manager{
		Config:               cfg,
		originalEngineConfig: cfg.Chain.Params.Engine,
	}

	logger, err := newLoggerFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("could not setup new logger instance, %w", err)
	}
	m.logger = logger.Named("server")

	m.logger.Info("Data dir", "path", cfg.DataDir)

	// Generate all the paths in the dataDir
	if err := common.SetupDataDir(cfg.DataDir, dirPaths, 0770); err != nil {
		return nil, fmt.Errorf("failed to create data directories: %w", err)
	}

	// prometheus
	if cfg.Telemetry.PrometheusAddr != nil {
		// Only setup telemetry if `PrometheusAddr` has been configured.
		if err := setupTelemetry(); err != nil {
			return nil, err
		}

		m.prometheusServer = startPrometheusServer(cfg.Telemetry.PrometheusAddr, m.logger)
	}

	// Set up datadog profiler
	if ddErr := enableDataDogProfiler(m.logger); err != nil {
		m.logger.Error("DataDog profiler setup failed", "err", ddErr.Error())
	}

	var db storage.Storage
	{
		if cfg.DataDir == "" {
			db, err = memory.NewMemoryStorage(nil)
			if err != nil {
				return nil, err
			}
		} else {
			db, err = leveldb.NewLevelDBStorage(
				filepath.Join(cfg.DataDir, "blockchain"),
				m.logger,
			)
			if err != nil {
				return nil, err
			}
		}
	}
	m.db = db
	m.storeContainer = jsonrpc.NewStoreContainer(db)

	// setup secrets
	secretsManager, err := setupSecretsManager(cfg, m.logger)
	if err != nil {
		return nil, err
	}
	m.secretsManager = secretsManager

	// blockchain object
	stateStorage, err := itrie.NewLevelDBStorage(filepath.Join(cfg.DataDir, "trie"), logger)
	if err != nil {
		return nil, err
	}
	m.stateStorage = stateStorage

	st := itrie.NewState(stateStorage)
	m.state = st

	return m, nil
}

func (m *Manager) Start() error {
	// the default hash store here will manage forked consensus engines and the
	// way they handle hashing of headers.  Each different engine as part of its factory
	// startup method will register itself in this instance
	consensus.DefaultHashStore.RegisterSelfAsHandler()

	// first analyse the forks and see if we need to do anything or just load up the default server
	if len(m.Config.Chain.Params.EngineForks) == 0 {
		m.logger.Info("no forks detected, running in simple mode")
		// no forks, just start the default server
		srv, err := m.createServer(m.Config, 0)
		if err != nil {
			return err
		}
		m.active = srv
		err = m.active.start()
		if err != nil {
			return err
		}
	} else {
		m.logger.Info("forks detected, running in fork mode")
		err := m.loadNextFork()
		if err != nil {
			return fmt.Errorf("failed to load next fork: %w", err)
		}
	}

	return nil
}

func (m *Manager) createServer(
	config *Config,
	forkNumber int,
) (*Server, error) {
	return NewManagedServer(
		config,
		m.logger,
		m.prometheusServer,
		m.secretsManager,
		m.stateStorage,
		m.state,
		m.db,
		forkNumber,
		m.storeContainer,
	)
}

// loadNextFork looks through the forks to find the starting point - forks should be ordered in the config file
// in the order you want to execute them
func (m *Manager) loadNextFork() error {
	// header could be available if we're in process at the fork, or if the application is starting
	// from cold we can read it from the database.
	var head uint64 = 0
	var header *types.Header
	if m.currentHeader != nil {
		head = m.currentHeader.Number
		header = m.currentHeader
	}
	if head == 0 {
		hash, ok := m.db.ReadHeadHash()
		if ok {
			h, err := m.db.ReadHeader(hash)
			if err != nil {
				return err
			}
			head = h.Number
			header = h
			header.Hash = hash // consensus can mess with this so take db version
		}
	}
	m.logger.Info("current head", "head", head)

	m.storeContainer.Reset()

	forks := m.Config.Chain.Params.EngineForks

	forkNumber := -1
	for _, fork := range forks {
		forkNumber++

		// found the one we want - so now to manipulate the config for creating the server
		// starting with the engine config
		m.nextFork = fork.To
		var found = false
		newEngineParams := make(map[string]interface{})
		for key, value := range m.originalEngineConfig {
			if key == fork.Engine {
				found = true
				newEngineParams[key] = value
				break
			}
		}
		if !found {
			return fmt.Errorf("failed to find engine %s in config", fork.Engine)
		}

		// overwrite the engine params with the new one only containing details for the active engine
		m.Config.Chain.Params.Engine = newEngineParams

		if err := m.handleForkGenesisOverrides(fork); err != nil {
			return err
		}

		if fork.To == nil {
			m.logger.Info("server manager loading engine fork", "engine", fork.Engine, "block", "nil")
		} else {
			m.logger.Info("server manager loading engine fork", "engine", fork.Engine, "block", *fork.To)
		}

		lastFork := m.getLastFork(forkNumber, forks)

		// ensure that the stop block is set.  This will help the syncer to stop at the correct height
		// and ensures that any delay in the stop channel being closed won't cause the chain to run on
		// beyond the fork point
		m.Config.Chain.Params.StopBlock = fork.To

		// create the server for side effects even if we don't intend on running it
		srv, err := m.createServer(m.Config, forkNumber)
		if err != nil {
			return err
		}

		if fork.To == nil || (fork.To != nil && head < *fork.To) {
			// only insert the fork block if we are at the exact fork point - we need to do this
			// after creating the server so the correct hashing algo is used for the block
			if forkNumber > 0 && lastFork.To != nil && head == *lastFork.To {
				if fork.Engine == "polybft" {
					err = m.insertPolybftForkBlock(fork, header, srv.blockchain, srv.executor)
					if err != nil {
						return err
					}
					m.logger.Info("Inserted polybft transition block", "height", header.Number+1)
					fmt.Println()
					fmt.Println(mascot)
					fmt.Println()
				}
			}

			m.active = srv

			m.monitorForNextFork()

			err = m.active.start()
			if err != nil {
				return err
			}

			break
		}
	}

	return nil
}

func (m *Manager) getLastFork(forkNumber int, forks []chain.EngineFork) chain.EngineFork {
	var lastFork chain.EngineFork
	if forkNumber > 0 {
		lastFork = forks[forkNumber-1]
		to := *lastFork.To
		m.Config.Chain.Params.ForkBlock = to + 1
	}
	return lastFork
}

func (m *Manager) handleForkGenesisOverrides(fork chain.EngineFork) error {
	if fork.BaseFee != nil {
		v, err := common.ParseUint64orHex(fork.BaseFee)
		if err != nil {
			return err
		}
		m.Config.Chain.Genesis.BaseFee = v
	}
	if fork.BaseFeeEM != nil {
		v, err := common.ParseUint64orHex(fork.BaseFeeEM)
		if err != nil {
			return err
		}
		m.Config.Chain.Genesis.BaseFeeEM = v
	}
	if fork.GasLimit != nil {
		v, err := common.ParseUint64orHex(fork.GasLimit)
		if err != nil {
			return err
		}
		m.Config.Chain.Genesis.GasLimit = v
	}
	if fork.GasUsed != nil {
		v, err := common.ParseUint64orHex(fork.GasUsed)
		if err != nil {
			return err
		}
		m.Config.Chain.Genesis.GasUsed = v
	}

	return nil
}

func (m *Manager) insertPolybftForkBlock(
	fork chain.EngineFork,
	currentHeader *types.Header,
	blockchain *blockchain.Blockchain,
	executor *state.Executor,
) error {
	if fork.Alloc == nil {
		// only interested if the fork block has allocs so we can handle the state root changees for them
		return nil
	}

	// compute genesis here will set the current block on the blockchain but it will be missing the hash
	// so we can set it with the one we already have
	err := blockchain.ComputeGenesis(false)
	if err != nil {
		return err
	}
	h := blockchain.Header()
	h.Hash = currentHeader.Hash

	if fork.To != nil && h.Number > *fork.To {
		// we must have already inserted this block so return
		return nil
	}

	newStateRoot, err := executor.WriteGenesis(fork.Alloc, currentHeader.StateRoot, true)
	if err != nil {
		return err
	}

	// now we have our genesis contracts in place we need to end the first epoch at the fork block
	epochCommit := &contractsapi.CommitEpochValidatorSetFn{
		ID: new(big.Int).SetUint64(1),
		Epoch: &contractsapi.Epoch{
			StartBlock: new(big.Int).SetUint64(1),
			EndBlock:   new(big.Int).SetUint64(h.Number),
			EpochRoot:  types.Hash{},
		},
	}

	input, err := epochCommit.EncodeAbi()
	if err != nil {
		return err
	}

	tx := &types.Transaction{
		From:     contracts.SystemCaller,
		To:       &contracts.ValidatorSetContract,
		Type:     types.StateTx,
		Input:    input,
		Gas:      types.StateTransactionGasLimit,
		GasPrice: big.NewInt(0),
	}
	tx.ComputeHash(h.Number)

	transition, err := executor.BeginTxn(newStateRoot, h, types.ZeroAddress)
	if err != nil {
		return err
	}

	err = transition.Write(tx)
	if err != nil {
		return err
	}

	_, newStateRoot, err = transition.Commit()
	if err != nil {
		return err
	}

	polyCfg, err := polybft.GetPolyBFTConfig(m.Config.Chain)
	if err != nil {
		return err
	}

	// insert the initial validator set into our mid-chain magic genesis
	oldValidators := validator.AccountSet{}
	newValidators := validator.AccountSet{}

	for _, v := range polyCfg.InitialValidatorSet {
		md, err := v.ToValidatorMetadata()
		if err != nil {
			return err
		}
		newValidators = append(newValidators, md)
	}

	vsd, err := validator.CreateValidatorSetDelta(oldValidators, newValidators)
	if err != nil {
		return err
	}

	extraData := polybft.Extra{
		Validators: vsd,
		Parent: &polybft.Signature{
			AggregatedSignature: []byte{},
			Bitmap:              []byte{},
		},
		Committed: &polybft.Signature{},
		Checkpoint: &polybft.CheckpointData{
			BlockRound:            1,
			EpochNumber:           2,
			CurrentValidatorsHash: types.Hash{},
			NextValidatorsHash:    types.Hash{},
			EventRoot:             types.Hash{},
		},
	}
	magic := []byte("magic_0x01")
	extraBytes := extraData.MarshalRLPTo(nil)
	copy(extraBytes, magic)

	header := &types.Header{
		ParentHash:   currentHeader.Hash,
		Sha3Uncles:   types.Hash{},
		Miner:        types.Address{}.Bytes(),
		StateRoot:    newStateRoot,
		TxRoot:       types.Hash{},
		ReceiptsRoot: types.Hash{},
		LogsBloom:    types.Bloom{},
		Difficulty:   1,
		Number:       currentHeader.Number + 1,
		GasLimit:     m.Config.Chain.Genesis.GasLimit,
		GasUsed:      m.Config.Chain.Genesis.GasUsed,
		Timestamp:    currentHeader.Timestamp + 1,
		ExtraData:    extraBytes,
		MixHash:      types.Hash{},
		Nonce:        types.Nonce{},
		Hash:         types.Hash{},
	}
	header.ComputeHash()

	block := &types.Block{
		Header:       header,
		Transactions: []*types.Transaction{},
		Uncles:       []*types.Header{},
	}

	fb := types.FullBlock{
		Block:    block,
		Receipts: []*types.Receipt{},
	}

	return blockchain.WriteFullBlock(&fb, "server_manager")
}

func (m *Manager) monitorForNextFork() {
	sub := m.active.blockchain.SubscribeEvents()

	go func() {
		for {
			// internally blocks until an event is received or the subscription is closed (returning nil)
			ev := sub.GetEvent()

			if ev == nil {
				// subscription closed
				break
			}

			header := ev.Header()
			m.currentHeader = header

			if m.nextFork != nil {
				m.logger.Debug("server manager monitored block", "number", header.Number, "next fork", *m.nextFork)
			} else {
				m.logger.Debug("server manager monitored block", "number", header.Number, "next fork", "nil")
			}

			if m.nextFork != nil && header.Number >= *m.nextFork {
				m.logger.Info("server manager reached next fork", "number", header.Number, "next fork", *m.nextFork)
				// we have reached the next fork point so stop the active server and create the next one
				m.active.Close()
				err := m.loadNextFork()
				if err != nil {
					m.logger.Error("failed to load next fork", "err", err)
					break
				}
			}
		}
	}()
}

func (m *Manager) Close() {
	m.active.Close()
	err := m.db.Close()
	if err != nil {
		m.logger.Error("failed to close database", "err", err)
	}
}
