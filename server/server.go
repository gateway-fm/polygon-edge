package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/umbracle/ethgo"
	"google.golang.org/grpc"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/leveldb"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/memory"
	consensusPolyBFT "github.com/0xPolygon/polygon-edge/consensus/polybft"
	"github.com/0xPolygon/polygon-edge/forkmanager"
	"github.com/0xPolygon/polygon-edge/gasprice"

	"github.com/0xPolygon/polygon-edge/archive"
	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/statesyncrelayer"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/wallet"
	"github.com/0xPolygon/polygon-edge/contracts"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/helper/progress"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/network"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/server/proto"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/state/runtime/addresslist"
	"github.com/0xPolygon/polygon-edge/state/runtime/tracer"
	"github.com/0xPolygon/polygon-edge/txpool"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validate"
)

var (
	errBlockTimeMissing = errors.New("block time configuration is missing")
	errBlockTimeInvalid = errors.New("block time configuration is invalid")
)

// Server is the central manager of the blockchain client
type Server struct {
	logger       hclog.Logger
	config       *Config
	state        state.State
	stateStorage itrie.Storage

	consensus consensus.Consensus

	// blockchain stack
	blockchain *blockchain.Blockchain
	chain      *chain.Chain

	// state executor
	executor *state.Executor

	// jsonrpc stack
	jsonrpcServer *jsonrpc.JSONRPC

	// system grpc server
	grpcServer *grpc.Server

	// libp2p network
	network *network.Server

	// transaction pool
	txpool *txpool.TxPool

	prometheusServer *http.Server

	// secrets manager
	secretsManager secrets.SecretsManager

	// restore
	restoreProgression *progress.ProgressionWrapper

	// stateSyncRelayer is handling state syncs execution (Polybft exclusive)
	stateSyncRelayer *statesyncrelayer.StateSyncRelayer

	// gasHelper is providing functions regarding gas and fees
	gasHelper *gasprice.GasHelper

	db storage.Storage

	jsonHub        *jsonRPCHub
	storeContainer *jsonrpc.StoreContainer
}

// newFileLogger returns logger instance that writes all logs to a specified file.
// If log file can't be created, it returns an error
func newFileLogger(config *Config) (hclog.Logger, error) {
	logFileWriter, err := os.Create(config.LogFilePath)
	if err != nil {
		return nil, fmt.Errorf("could not create log file, %w", err)
	}

	return hclog.New(&hclog.LoggerOptions{
		Name:       "polygon",
		Level:      config.LogLevel,
		Output:     logFileWriter,
		JSONFormat: config.JSONLogFormat,
	}), nil
}

// newCLILogger returns minimal logger instance that sends all logs to standard output
func newCLILogger(config *Config) hclog.Logger {
	return hclog.New(&hclog.LoggerOptions{
		Name:       "polygon",
		Level:      config.LogLevel,
		JSONFormat: config.JSONLogFormat,
	})
}

// newLoggerFromConfig creates a new logger which logs to a specified file.
// If log file is not set it outputs to standard output ( console ).
// If log file is specified, and it can't be created the server command will error out
func newLoggerFromConfig(config *Config) (hclog.Logger, error) {
	if config.LogFilePath != "" {
		fileLoggerInstance, err := newFileLogger(config)
		if err != nil {
			return nil, err
		}

		return fileLoggerInstance, nil
	}

	return newCLILogger(config), nil
}

func NewManagedServer(
	config *Config,
	logger hclog.Logger,
	prometheusServer *http.Server,
	secretsManager secrets.SecretsManager,
	stateStorage itrie.Storage,
	st *itrie.State,
	db storage.Storage,
	forkNumber int,
	storeContainer *jsonrpc.StoreContainer,
) (*Server, error) {
	m := &Server{
		config:             config,
		logger:             logger,
		prometheusServer:   prometheusServer,
		secretsManager:     secretsManager,
		stateStorage:       stateStorage,
		state:              st,
		db:                 db,
		grpcServer:         grpc.NewServer(grpc.UnaryInterceptor(unaryInterceptor)),
		storeContainer:     storeContainer,
		restoreProgression: progress.NewProgressionWrapper(progress.ChainSyncRestore),
	}

	m.executor = state.NewExecutor(config.Chain.Params, st, logger)
	engineName := m.config.Chain.Params.GetEngine()
	m.engineChecks(engineName)
	return m.build(engineName, forkNumber)
}

// NewServer creates a new Minimal server, using the passed in configuration
func NewServer(config *Config) (*Server, error) {
	logger, err := newLoggerFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("could not setup new logger instance, %w", err)
	}

	m := &Server{
		logger:             logger.Named("server"),
		config:             config,
		chain:              config.Chain,
		grpcServer:         grpc.NewServer(grpc.UnaryInterceptor(unaryInterceptor)),
		restoreProgression: progress.NewProgressionWrapper(progress.ChainSyncRestore),
	}

	m.logger.Info("Data dir", "path", config.DataDir)

	// Generate all the paths in the dataDir
	if err := common.SetupDataDir(config.DataDir, dirPaths, 0770); err != nil {
		return nil, fmt.Errorf("failed to create data directories: %w", err)
	}

	if config.Telemetry.PrometheusAddr != nil {
		// Only setup telemetry if `PrometheusAddr` has been configured.
		if err := setupTelemetry(); err != nil {
			return nil, err
		}

		m.prometheusServer = startPrometheusServer(config.Telemetry.PrometheusAddr, m.logger)
	}

	// Set up datadog profiler
	if ddErr := enableDataDogProfiler(m.logger); err != nil {
		m.logger.Error("DataDog profiler setup failed", "err", ddErr.Error())
	}

	// Set up the secrets manager
	secretsManager, err := setupSecretsManager(config, m.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to set up the secrets manager: %w", err)
	}
	m.secretsManager = secretsManager

	// start libp2p
	{
		netConfig := config.Network
		netConfig.Chain = m.config.Chain
		netConfig.DataDir = filepath.Join(m.config.DataDir, "libp2p")
		netConfig.SecretsManager = m.secretsManager

		network, err := network.NewServer(logger, netConfig)
		if err != nil {
			return nil, err
		}
		m.network = network
	}

	// start blockchain object
	stateStorage, err := itrie.NewLevelDBStorage(filepath.Join(m.config.DataDir, "trie"), logger)
	if err != nil {
		return nil, err
	}
	m.stateStorage = stateStorage

	st := itrie.NewState(stateStorage)
	m.state = st

	// create storage instance for blockchain
	var db storage.Storage
	{
		if m.config.DataDir == "" {
			db, err = memory.NewMemoryStorage(nil)
			if err != nil {
				return nil, err
			}
		} else {
			db, err = leveldb.NewLevelDBStorage(
				filepath.Join(m.config.DataDir, "blockchain"),
				m.logger,
			)
			if err != nil {
				return nil, err
			}
		}
	}

	m.db = db
	m.storeContainer = jsonrpc.NewStoreContainer(db)

	m.executor = state.NewExecutor(config.Chain.Params, st, logger)

	engineName := m.config.Chain.Params.GetEngine()
	m.engineChecks(engineName)

	srv, err := m.build(engineName, 0)
	if err != nil {
		return nil, err
	}

	return srv, srv.start()
}

func (m *Server) engineChecks(engineName string) {
	// custom write genesis hook per consensus engine
	if factory, exists := genesisCreationFactory[ConsensusType(engineName)]; exists {
		m.executor.GenesisPostHook = factory(m.config.Chain, engineName)
	}

	// apply allow list contracts deployer genesis data
	if m.config.Chain.Params.ContractDeployerAllowList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.AllowListContractsAddr,
			m.config.Chain.Params.ContractDeployerAllowList)
	}

	// apply block list contracts deployer genesis data
	if m.config.Chain.Params.ContractDeployerBlockList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.BlockListContractsAddr,
			m.config.Chain.Params.ContractDeployerBlockList)
	}

	// apply transactions execution allow list genesis data
	if m.config.Chain.Params.TransactionsAllowList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.AllowListTransactionsAddr,
			m.config.Chain.Params.TransactionsAllowList)
	}

	// apply transactions execution block list genesis data
	if m.config.Chain.Params.TransactionsBlockList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.BlockListTransactionsAddr,
			m.config.Chain.Params.TransactionsBlockList)
	}

	// apply bridge allow list genesis data
	if m.config.Chain.Params.BridgeAllowList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.AllowListBridgeAddr,
			m.config.Chain.Params.BridgeAllowList)
	}

	// apply bridge block list genesis data
	if m.config.Chain.Params.BridgeBlockList != nil {
		addresslist.ApplyGenesisAllocs(m.config.Chain.Genesis, contracts.BlockListBridgeAddr,
			m.config.Chain.Params.BridgeBlockList)
	}
}

func (m *Server) build(
	engineName string,
	forkNumber int,
) (*Server, error) {
	initialStateRoot := types.ZeroHash

	if ConsensusType(engineName) == PolyBFTConsensus {
		polyBFTConfig, err := consensusPolyBFT.GetPolyBFTConfig(m.config.Chain)
		if err != nil {
			return nil, err
		}

		if polyBFTConfig.InitialTrieRoot != types.ZeroHash {
			checkedInitialTrieRoot, err := itrie.HashChecker(polyBFTConfig.InitialTrieRoot.Bytes(), m.stateStorage)
			if err != nil {
				return nil, fmt.Errorf("error on state root verification %w", err)
			}

			if checkedInitialTrieRoot != polyBFTConfig.InitialTrieRoot {
				return nil, errors.New("invalid initial state root")
			}

			m.logger.Info("Initial state root checked and correct")

			initialStateRoot = polyBFTConfig.InitialTrieRoot
		}
	}

	// after we have our initial genesis forks further down the line may need to
	// use hooks, but we must execute these after the genesis root calculation
	// so that this stays true even though we will be adjusting the state root
	// shortly after
	isInitialFork := forkNumber == 0

	// handle the genesis root before we process any additional allocs as these will adjust
	// the state root throwing out hash checks later on
	genesisRoot, err := m.executor.WriteGenesis(m.config.Chain.Genesis.Alloc, initialStateRoot, isInitialFork)
	if err != nil {
		return nil, err
	}
	m.config.Chain.Genesis.StateRoot = genesisRoot

	if err := initForkManager(engineName, m.config.Chain); err != nil {
		return nil, err
	}

	// Use the london signer with eip-155 as a fallback one
	var signer crypto.TxSigner = crypto.NewLondonSigner(
		uint64(m.config.Chain.Params.ChainID),
		m.config.Chain.Params.Forks.IsActive(chain.Homestead, 0),
		crypto.NewEIP155Signer(
			uint64(m.config.Chain.Params.ChainID),
			m.config.Chain.Params.Forks.IsActive(chain.Homestead, 0),
		),
	)

	// blockchain object
	m.blockchain, err = blockchain.NewBlockchain(
		m.logger,
		m.db,
		m.config.Chain,
		nil,
		m.executor,
		signer,
	)
	if err != nil {
		return nil, err
	}

	// here we can provide some other configuration
	m.gasHelper, err = gasprice.NewGasHelper(gasprice.DefaultGasHelperConfig, m.blockchain)
	if err != nil {
		return nil, err
	}

	m.executor.GetHash = m.blockchain.GetHashHelper

	// setup libp2p
	netConfig := m.config.Network
	netConfig.Chain = m.config.Chain
	netConfig.DataDir = filepath.Join(m.config.DataDir, "libp2p")
	netConfig.SecretsManager = m.secretsManager

	libp2p, err := network.NewServer(m.logger, netConfig)
	if err != nil {
		return nil, err
	}
	m.network = libp2p

	{
		hub := &txpoolHub{
			state:      m.state,
			Blockchain: m.blockchain,
		}

		// start transaction pool
		m.txpool, err = txpool.NewTxPool(
			m.logger,
			m.config.Chain.Params.Forks.At(0),
			hub,
			m.grpcServer,
			m.network,
			&txpool.Config{
				MaxSlots:           m.config.MaxSlots,
				PriceLimit:         m.config.PriceLimit,
				MaxAccountEnqueued: m.config.MaxAccountEnqueued,
				ChainID:            big.NewInt(m.config.Chain.Params.ChainID),
			},
		)
		if err != nil {
			return nil, err
		}

		m.txpool.SetSigner(signer)
	}

	{
		// Setup consensus
		if err := m.setupConsensus(forkNumber > 0); err != nil {
			return nil, err
		}
		m.blockchain.SetConsensus(m.consensus)
	}

	// after consensus is done, we can mine the genesis block in blockchain
	// This is done because consensus might use a custom Hash function so we need
	// to wait for consensus because we do any block hashing like genesis
	if err := m.blockchain.ComputeGenesis(forkNumber == 0); err != nil {
		return nil, err
	}

	// create the json hub prior to running the endpoint as this server may not be started
	// in the case of a fork
	m.jsonHub = &jsonRPCHub{
		state:              m.state,
		restoreProgression: m.restoreProgression,
		Blockchain:         m.blockchain,
		TxPool:             m.txpool,
		Executor:           m.executor,
		Consensus:          m.consensus,
		Server:             m.network,
		GasStore:           m.gasHelper,
	}

	m.storeContainer.AddStore(m.jsonHub, m.config.Chain.Params.StopBlock)

	return m, nil
}

func (m *Server) start() error {
	// initialize data in consensus layer
	if err := m.consensus.Initialize(); err != nil {
		return err
	}

	// setup and start grpc server
	if err := m.setupGRPC(); err != nil {
		return err
	}

	if err := m.network.Start(); err != nil {
		return err
	}

	// setup and start jsonrpc server
	if err := m.setupJSONRPC(); err != nil {
		return err
	}

	// restore archive data before starting
	if err := m.restoreChain(); err != nil {
		return err
	}

	if err := m.consensus.Start(); err != nil {
		return err
	}

	// start relayer
	if m.config.Relayer {
		if err := m.setupRelayer(); err != nil {
			return err
		}
	}

	m.txpool.Start()

	return nil
}

func unaryInterceptor(
	ctx context.Context,
	req interface{},
	_ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	// Validate request
	if err := validate.ValidateRequest(req); err != nil {
		return nil, err
	}

	return handler(ctx, req)
}

func (s *Server) restoreChain() error {
	if s.config.RestoreFile == nil {
		return nil
	}

	if err := archive.RestoreChain(s.blockchain, *s.config.RestoreFile, s.restoreProgression); err != nil {
		return err
	}

	return nil
}

type txpoolHub struct {
	state state.State
	*blockchain.Blockchain
}

// getAccountImpl is used for fetching account state from both TxPool and JSON-RPC
func getAccountImpl(state state.State, root types.Hash, addr types.Address) (*state.Account, error) {
	snap, err := state.NewSnapshotAt(root)
	if err != nil {
		return nil, fmt.Errorf("unable to get snapshot for root '%s': %w", root, err)
	}

	account, err := snap.GetAccount(addr)
	if err != nil {
		return nil, err
	}

	if account == nil {
		return nil, jsonrpc.ErrStateNotFound
	}

	return account, nil
}

func (t *txpoolHub) GetNonce(root types.Hash, addr types.Address) uint64 {
	account, err := getAccountImpl(t.state, root, addr)

	if err != nil {
		return 0
	}

	return account.Nonce
}

func (t *txpoolHub) GetBalance(root types.Hash, addr types.Address) (*big.Int, error) {
	account, err := getAccountImpl(t.state, root, addr)

	if err != nil {
		if errors.Is(err, jsonrpc.ErrStateNotFound) {
			return big.NewInt(0), nil
		}

		return big.NewInt(0), err
	}

	return account.Balance, nil
}

// setupSecretsManager sets up the secrets manager
func setupSecretsManager(config *Config, logger hclog.Logger) (secrets.SecretsManager, error) {
	secretsManagerConfig := config.SecretsManager
	if secretsManagerConfig == nil {
		// No config provided, use default
		secretsManagerConfig = &secrets.SecretsManagerConfig{
			Type: secrets.Local,
		}
	}

	secretsManagerType := secretsManagerConfig.Type
	secretsManagerParams := &secrets.SecretsManagerParams{
		Logger: logger,
	}

	if secretsManagerType == secrets.Local {
		// Only the base directory is required for
		// the local secrets manager
		secretsManagerParams.Extra = map[string]interface{}{
			secrets.Path: config.DataDir,
		}
	}

	// Grab the factory method
	secretsManagerFactory, ok := secretsManagerBackends[secretsManagerType]
	if !ok {
		return nil, fmt.Errorf("secrets manager type '%s' not found", secretsManagerType)
	}

	// Instantiate the secrets manager
	secretsManager, factoryErr := secretsManagerFactory(
		secretsManagerConfig,
		secretsManagerParams,
	)

	if factoryErr != nil {
		return nil, fmt.Errorf("unable to instantiate secrets manager, %w", factoryErr)
	}

	return secretsManager, nil
}

// setupConsensus sets up the consensus mechanism
func (s *Server) setupConsensus(
	fromForked bool,
) error {
	engineName := s.config.Chain.Params.GetEngine()
	engine, ok := consensusBackends[ConsensusType(engineName)]

	if !ok {
		return fmt.Errorf("consensus engine '%s' not found", engineName)
	}

	engineConfig, ok := s.config.Chain.Params.Engine[engineName].(map[string]interface{})
	if !ok {
		engineConfig = map[string]interface{}{}
	}

	var (
		blockTime = common.Duration{Duration: 0}
		err       error
	)

	if engineName != string(DummyConsensus) && engineName != string(DevConsensus) {
		blockTime, err = extractBlockTime(engineConfig)
		if err != nil {
			return err
		}
	}

	engineConfig["devp2p-peers"] = s.config.Chain.DevP2PBootnodes
	engineConfig["devp2p-addr"] = s.config.DevP2PAddr

	config := &consensus.Config{
		Params: s.config.Chain.Params,
		Config: engineConfig,
		Path:   filepath.Join(s.config.DataDir, "consensus"),
	}

	consensus, err := engine(
		&consensus.Params{
			Context:               context.Background(),
			Config:                config,
			TxPool:                s.txpool,
			Network:               s.network,
			Blockchain:            s.blockchain,
			Executor:              s.executor,
			Grpc:                  s.grpcServer,
			Logger:                s.logger,
			SecretsManager:        s.secretsManager,
			BlockTime:             uint64(blockTime.Seconds()),
			NumBlockConfirmations: s.config.NumBlockConfirmations,
			FromForked:            fromForked,
		},
	)

	if err != nil {
		return err
	}

	s.consensus = consensus

	return nil
}

// setupRelayer sets up the relayer
func (s *Server) setupRelayer() error {
	account, err := wallet.NewAccountFromSecret(s.secretsManager)
	if err != nil {
		return fmt.Errorf("failed to create account from secret: %w", err)
	}

	polyBFTConfig, err := consensusPolyBFT.GetPolyBFTConfig(s.config.Chain)
	if err != nil {
		return fmt.Errorf("failed to extract polybft config: %w", err)
	}

	trackerStartBlockConfig := map[types.Address]uint64{}
	if polyBFTConfig.Bridge != nil {
		trackerStartBlockConfig = polyBFTConfig.Bridge.EventTrackerStartBlocks
	}

	relayer := statesyncrelayer.NewRelayer(
		s.config.DataDir,
		s.config.JSONRPC.JSONRPCAddr.String(),
		ethgo.Address(contracts.StateReceiverContract),
		trackerStartBlockConfig[contracts.StateReceiverContract],
		s.logger.Named("relayer"),
		wallet.NewEcdsaSigner(wallet.NewKey(account)),
	)

	// start relayer
	if err := relayer.Start(); err != nil {
		return fmt.Errorf("failed to start relayer: %w", err)
	}

	return nil
}

type jsonRPCHub struct {
	state              state.State
	restoreProgression *progress.ProgressionWrapper

	consensus.Consensus

	*blockchain.Blockchain
	*txpool.TxPool
	*state.Executor
	*network.Server
	gasprice.GasStore
}

func (j *jsonRPCHub) BridgeDataProvider() consensus.BridgeDataProvider {
	return j.Consensus.GetBridgeProvider()
}

func (j *jsonRPCHub) GetPeers() int {
	return len(j.Server.Peers())
}

func (j *jsonRPCHub) GetAccount(root types.Hash, addr types.Address) (*jsonrpc.Account, error) {
	acct, err := getAccountImpl(j.state, root, addr)
	if err != nil {
		return nil, err
	}

	account := &jsonrpc.Account{
		Nonce:   acct.Nonce,
		Balance: new(big.Int).Set(acct.Balance),
	}

	return account, nil
}

// GetForksInTime returns the active forks at the given block height
func (j *jsonRPCHub) GetForksInTime(blockNumber uint64) chain.ForksInTime {
	return j.Executor.GetForksInTime(blockNumber)
}

func (j *jsonRPCHub) GetStorage(stateRoot types.Hash, addr types.Address, slot types.Hash) ([]byte, error) {
	account, err := getAccountImpl(j.state, stateRoot, addr)
	if err != nil {
		return nil, err
	}

	snap, err := j.state.NewSnapshotAt(stateRoot)
	if err != nil {
		return nil, err
	}

	res := snap.GetStorage(addr, account.Root, slot)

	return res.Bytes(), nil
}

func (j *jsonRPCHub) GetCode(root types.Hash, addr types.Address) ([]byte, error) {
	account, err := getAccountImpl(j.state, root, addr)
	if err != nil {
		return nil, err
	}

	code, ok := j.state.GetCode(types.BytesToHash(account.CodeHash))
	if !ok {
		return nil, fmt.Errorf("unable to fetch code")
	}

	return code, nil
}

func (j *jsonRPCHub) ApplyTxn(
	header *types.Header,
	txn *types.Transaction,
	override types.StateOverride,
) (result *runtime.ExecutionResult, err error) {
	blockCreator, err := j.GetConsensus().GetBlockCreator(header)
	if err != nil {
		return nil, err
	}

	transition, err := j.BeginTxn(header.StateRoot, header, blockCreator)
	if err != nil {
		return
	}

	if override != nil {
		if err = transition.WithStateOverride(override); err != nil {
			return
		}
	}

	result, err = transition.Apply(txn)

	return
}

// TraceBlock traces all transactions in the given block and returns all results
func (j *jsonRPCHub) TraceBlock(
	block *types.Block,
	tracer tracer.Tracer,
) ([]interface{}, error) {
	if block.Number() == 0 {
		return nil, errors.New("genesis block can't have transaction")
	}

	parentHeader, ok := j.GetHeaderByHash(block.ParentHash())
	if !ok {
		return nil, errors.New("parent header not found")
	}

	blockCreator, err := j.GetConsensus().GetBlockCreator(block.Header)
	if err != nil {
		return nil, err
	}

	transition, err := j.BeginTxn(parentHeader.StateRoot, block.Header, blockCreator)
	if err != nil {
		return nil, err
	}

	transition.SetTracer(tracer)

	results := make([]interface{}, len(block.Transactions))

	for idx, tx := range block.Transactions {
		tracer.Clear()

		if _, err := transition.Apply(tx); err != nil {
			return nil, err
		}

		if results[idx], err = tracer.GetResult(); err != nil {
			return nil, err
		}
	}

	return results, nil
}

// TraceTxn traces a transaction in the block, associated with the given hash
func (j *jsonRPCHub) TraceTxn(
	block *types.Block,
	targetTxHash types.Hash,
	tracer tracer.Tracer,
) (interface{}, error) {
	if block.Number() == 0 {
		return nil, errors.New("genesis block can't have transaction")
	}

	parentHeader, ok := j.GetHeaderByHash(block.ParentHash())
	if !ok {
		return nil, errors.New("parent header not found")
	}

	blockCreator, err := j.GetConsensus().GetBlockCreator(block.Header)
	if err != nil {
		return nil, err
	}

	transition, err := j.BeginTxn(parentHeader.StateRoot, block.Header, blockCreator)
	if err != nil {
		return nil, err
	}

	var targetTx *types.Transaction

	for _, tx := range block.Transactions {
		if tx.Hash == targetTxHash {
			targetTx = tx

			break
		}

		// Execute transactions without tracer until reaching the target transaction
		if _, err := transition.Apply(tx); err != nil {
			return nil, err
		}
	}

	if targetTx == nil {
		return nil, errors.New("target tx not found")
	}

	transition.SetTracer(tracer)

	if _, err := transition.Apply(targetTx); err != nil {
		return nil, err
	}

	return tracer.GetResult()
}

func (j *jsonRPCHub) TraceCall(
	tx *types.Transaction,
	parentHeader *types.Header,
	tracer tracer.Tracer,
) (interface{}, error) {
	blockCreator, err := j.GetConsensus().GetBlockCreator(parentHeader)
	if err != nil {
		return nil, err
	}

	transition, err := j.BeginTxn(parentHeader.StateRoot, parentHeader, blockCreator)
	if err != nil {
		return nil, err
	}

	transition.SetTracer(tracer)

	if _, err := transition.Apply(tx); err != nil {
		return nil, err
	}

	return tracer.GetResult()
}

func (j *jsonRPCHub) GetSyncProgression() *progress.Progression {
	// restore progression
	if restoreProg := j.restoreProgression.GetProgression(); restoreProg != nil {
		return restoreProg
	}

	// consensus sync progression
	if consensusSyncProg := j.Consensus.GetSyncProgression(); consensusSyncProg != nil {
		return consensusSyncProg
	}

	return nil
}

// SETUP //

// setupJSONRCP sets up the JSONRPC server, using the set configuration
func (s *Server) setupJSONRPC() error {
	conf := &jsonrpc.Config{
		StoreContainer:           s.storeContainer,
		Addr:                     s.config.JSONRPC.JSONRPCAddr,
		ChainID:                  uint64(s.config.Chain.Params.ChainID),
		ChainName:                s.config.Chain.Name,
		AccessControlAllowOrigin: s.config.JSONRPC.AccessControlAllowOrigin,
		PriceLimit:               s.config.PriceLimit,
		BatchLengthLimit:         s.config.JSONRPC.BatchLengthLimit,
		BlockRangeLimit:          s.config.JSONRPC.BlockRangeLimit,
	}

	srv, err := jsonrpc.NewJSONRPC(s.logger, conf)
	if err != nil {
		return err
	}

	s.jsonrpcServer = srv

	return nil
}

// setupGRPC sets up the grpc server and listens on tcp
func (s *Server) setupGRPC() error {
	proto.RegisterSystemServer(s.grpcServer, &systemService{server: s})

	lis, err := net.Listen("tcp", s.config.GRPCAddr.String())
	if err != nil {
		return err
	}

	// Start server with infinite retries
	go func() {
		if err := s.grpcServer.Serve(lis); err != nil {
			s.logger.Error(err.Error())
		}
	}()

	s.logger.Info("GRPC server running", "addr", s.config.GRPCAddr.String())

	return nil
}

// Chain returns the chain object of the client
func (s *Server) Chain() *chain.Chain {
	return s.chain
}

// JoinPeer attempts to add a new peer to the networking server
func (s *Server) JoinPeer(rawPeerMultiaddr string) error {
	return s.network.JoinPeer(rawPeerMultiaddr)
}

// Close closes the Minimal server (blockchain, networking, consensus)
func (s *Server) Close() {
	// Close the consensus layer
	if err := s.consensus.Close(); err != nil {
		s.logger.Error("failed to close consensus", "err", err.Error())
	}

	// Close the blockchain layer
	//if err := s.blockchain.Close(); err != nil {
	//	s.logger.Error("failed to close blockchain", "err", err.Error())
	//}

	// Close the networking layer
	if err := s.network.Close(); err != nil {
		s.logger.Error("failed to close networking", "err", err.Error())
	}

	s.grpcServer.Stop()
	s.jsonrpcServer.Stop()

	// Close the state storage
	//if err := s.stateStorage.Close(); err != nil {
	//	s.logger.Error("failed to close storage for trie", "err", err.Error())
	//}

	if s.prometheusServer != nil {
		if err := s.prometheusServer.Shutdown(context.Background()); err != nil {
			s.logger.Error("Prometheus server shutdown error", err)
		}
	}

	// Stop state sync relayer
	if s.stateSyncRelayer != nil {
		s.stateSyncRelayer.Stop()
	}

	// Close the txpool's main loop
	s.txpool.Close()

	// Close DataDog profiler
	s.closeDataDogProfiler()
}

// Entry is a consensus configuration entry
type Entry struct {
	Enabled bool
	Config  map[string]interface{}
}

func startPrometheusServer(listenAddr *net.TCPAddr, logger hclog.Logger) *http.Server {
	srv := &http.Server{
		Addr: listenAddr.String(),
		Handler: promhttp.InstrumentMetricHandler(
			prometheus.DefaultRegisterer, promhttp.HandlerFor(
				prometheus.DefaultGatherer,
				promhttp.HandlerOpts{},
			),
		),
		ReadHeaderTimeout: 60 * time.Second,
	}

	logger.Info("Prometheus server started", "addr=", listenAddr.String())

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				logger.Error("Prometheus HTTP server ListenAndServe", "err", err)
			}
		}
	}()

	return srv
}

func initForkManager(engineName string, config *chain.Chain) error {
	var initialParams *forkmanager.ForkParams

	if factory := forkManagerInitialParamsFactory[ConsensusType(engineName)]; factory != nil {
		params, err := factory(config)
		if err != nil {
			return err
		}

		initialParams = params
	}

	fm := forkmanager.GetInstance()

	// clear everything in forkmanager (if there was something because of tests) and register initial fork
	fm.Clear()
	fm.RegisterFork(forkmanager.InitialFork, initialParams)

	// Register forks
	for name, f := range *config.Params.Forks {
		// check if fork is not supported by current edge version
		if _, found := (*chain.AllForksEnabled)[name]; !found {
			return fmt.Errorf("fork is not available: %s", name)
		}

		fm.RegisterFork(name, f.Params)
	}

	// Register handlers and additional forks here
	if err := types.RegisterTxHashFork(chain.TxHashWithType); err != nil {
		return err
	}

	if factory := forkManagerFactory[ConsensusType(engineName)]; factory != nil {
		if err := factory(config.Params.Forks); err != nil {
			return err
		}
	}

	// Activate initial fork
	if err := fm.ActivateFork(forkmanager.InitialFork, uint64(0)); err != nil {
		return err
	}

	// Activate forks
	for name, f := range *config.Params.Forks {
		if err := fm.ActivateFork(name, f.Block); err != nil {
			return err
		}
	}

	return nil
}

// extractBlockTime extracts blockTime parameter from consensus engine configuration.
// If it is missing or invalid, an appropriate error is returned.
func extractBlockTime(engineConfig map[string]interface{}) (common.Duration, error) {
	blockTimeGeneric, ok := engineConfig["blockTime"]
	if !ok {
		return common.Duration{}, errBlockTimeMissing
	}

	blockTimeRaw, err := json.Marshal(blockTimeGeneric)
	if err != nil {
		return common.Duration{}, errBlockTimeInvalid
	}

	var blockTime common.Duration

	if err := json.Unmarshal(blockTimeRaw, &blockTime); err != nil {
		return common.Duration{}, errBlockTimeInvalid
	}

	if blockTime.Seconds() < 1 {
		return common.Duration{}, errBlockTimeInvalid
	}

	return blockTime, nil
}
