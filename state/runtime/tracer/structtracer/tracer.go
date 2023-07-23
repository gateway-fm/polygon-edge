package structtracer

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/0xPolygon/polygon-edge/helper/hex"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/state/runtime/evm"
	"github.com/0xPolygon/polygon-edge/state/runtime/tracer"
	"github.com/0xPolygon/polygon-edge/types"
)

type Config struct {
	EnableMemory     bool // enable memory capture
	EnableStack      bool // enable stack capture
	EnableStorage    bool // enable storage capture
	EnableReturnData bool // enable return data capture
	Legacy           bool // legacy mode
}

type StructLog struct {
	Pc            uint64                    `json:"pc"`
	Op            string                    `json:"op"`
	Gas           uint64                    `json:"gas"`
	GasCost       uint64                    `json:"gasCost"`
	Memory        []byte                    `json:"memory,omitempty"`
	MemorySize    int                       `json:"memSize"`
	Stack         []*big.Int                `json:"stack"`
	ReturnData    []byte                    `json:"returnData,omitempty"`
	Storage       map[types.Hash]types.Hash `json:"storage"`
	Depth         int                       `json:"depth"`
	RefundCounter uint64                    `json:"refund"`
	Err           error                     `json:"err"`
	Reason        *string                   `json:"reason"`
}

type callFrame struct {
	depth      int
	callType   int
	gas        uint64
	callFrames []*callFrame
	logs       []StructLog
	index      int
}

func (l *StructLog) ErrorString() string {
	if l.Err != nil {
		return l.Err.Error()
	}

	return ""
}

type StructTracer struct {
	Config Config

	cancelLock sync.RWMutex
	reason     error
	interrupt  bool

	logs        []StructLog
	gasLimit    uint64
	consumedGas uint64
	output      []byte
	err         error

	storage       []map[types.Address]map[types.Hash]types.Hash
	currentMemory []([]byte)
	currentStack  []([]*big.Int)
	callStack     []*callFrame
	justExited    bool
}

func NewStructTracer(config Config) *StructTracer {
	storage := make([](map[types.Address]map[types.Hash]types.Hash), 1)
	storage[0] = make(map[types.Address]map[types.Hash]types.Hash)

	return &StructTracer{
		Config:        config,
		cancelLock:    sync.RWMutex{},
		storage:       storage,
		currentMemory: make([]([]byte), 1),
		currentStack:  make([]([]*big.Int), 1),
		callStack:     []*callFrame{},
	}
}

func (t *StructTracer) Cancel(err error) {
	t.cancelLock.Lock()
	defer t.cancelLock.Unlock()

	t.reason = err
	t.interrupt = true
}

func (t *StructTracer) cancelled() bool {
	t.cancelLock.RLock()
	defer t.cancelLock.RUnlock()

	return t.interrupt
}

func (t *StructTracer) Clear() {
	t.reason = nil
	t.interrupt = false
	t.logs = t.logs[:0]
	t.gasLimit = 0
	t.consumedGas = 0
	t.output = t.output[:0]
	t.err = nil
	t.storage = make([](map[types.Address]map[types.Hash]types.Hash), 1)
	t.storage[0] = make(map[types.Address]map[types.Hash]types.Hash)
	t.currentMemory = make([]([]byte), 1)
	t.currentStack = make([]([]*big.Int), 1)
}

func (t *StructTracer) TxStart(gasLimit uint64) {
	t.gasLimit = gasLimit
}

func (t *StructTracer) TxEnd(gasLeft uint64) {
	t.consumedGas = t.gasLimit - gasLeft
}

func (t *StructTracer) CallStart(
	depth int,
	from, to types.Address,
	callType int,
	gas uint64,
	value *big.Int,
	input []byte,
) {
	frame := &callFrame{
		depth:      depth,
		callType:   callType,
		gas:        gas,
		callFrames: []*callFrame{},
		logs:       []StructLog{},
	}

	t.callStack = append(t.callStack, frame)
}

func (t *StructTracer) CallEnd(
	depth int,
	output []byte,
	err error,
) {

	if depth == 1 {
		t.output = output
		t.err = err
	} else {
		t.justExited = true
	}
}

func (t *StructTracer) CaptureState(
	memory []byte,
	stack []*big.Int,
	opCode int,
	contractAddress types.Address,
	sp int,
	host tracer.RuntimeHost,
	state tracer.VMState,
) {
	if t.cancelled() {
		state.Halt()

		return
	}

	t.captureMemory(memory, opCode)

	t.captureStack(stack, sp, opCode)

	t.captureStorage(
		stack,
		opCode,
		contractAddress,
		sp,
		host,
	)
}

func (t *StructTracer) captureMemory(
	memory []byte,
	opCode int,
) {
	if !t.Config.EnableMemory {
		return
	}

	// always allocate new space to get new reference
	currentMemory := make([]byte, len(memory))
	copy(currentMemory, memory)

	t.currentMemory[len(t.currentMemory)-1] = currentMemory

	if opCode == evm.CALL || opCode == evm.STATICCALL {
		t.currentMemory = append(t.currentMemory, make([]byte, len(memory)))
	}
}

func (t *StructTracer) captureStack(
	stack []*big.Int,
	sp int,
	opCode int,
) {
	if !t.Config.EnableStack {
		return
	}

	currentStack := make([]*big.Int, sp)

	for i, v := range stack {
		if i >= sp {
			break
		}

		currentStack[i] = new(big.Int).Set(v)
	}

	t.currentStack[len(t.currentStack)-1] = currentStack

	if opCode == evm.CALL || opCode == evm.STATICCALL {
		t.currentStack = append(t.currentStack, make([]*big.Int, sp))
	}
}

func (t *StructTracer) captureStorage(
	stack []*big.Int,
	opCode int,
	contractAddress types.Address,
	sp int,
	host tracer.RuntimeHost,
) {
	if opCode == evm.CALL || opCode == evm.STATICCALL {
		t.storage = append(t.storage, make(map[types.Address]map[types.Hash]types.Hash))
	}

	if !t.Config.EnableStorage || (opCode != evm.SLOAD && opCode != evm.SSTORE) {
		return
	}

	storage := &t.storage[len(t.storage)-1]
	_, initialized := (*storage)[contractAddress]

	switch opCode {
	case evm.SLOAD:
		if sp < 1 {
			return
		}

		if !initialized {
			(*storage)[contractAddress] = make(map[types.Hash]types.Hash)
		}

		slot := types.BytesToHash(stack[sp-1].Bytes())
		value := host.GetStorage(contractAddress, slot)

		(*storage)[contractAddress][slot] = value

	case evm.SSTORE:
		if sp < 2 {
			return
		}

		if !initialized {
			(*storage)[contractAddress] = make(map[types.Hash]types.Hash)
		}

		slot := types.BytesToHash(stack[sp-1].Bytes())
		value := types.BytesToHash(stack[sp-2].Bytes())

		(*storage)[contractAddress][slot] = value
	}
}

func (t *StructTracer) ExecuteState(
	contractAddress types.Address,
	ip uint64,
	opCode string,
	availableGas uint64,
	cost uint64,
	lastReturnData []byte,
	depth int,
	err error,
	host tracer.RuntimeHost,
) {
	var (
		memory     []byte
		memorySize int
		stack      []*big.Int
		returnData []byte
		storage    map[types.Hash]types.Hash
	)

	if t.Config.EnableMemory {
		if opCode == evm.OpCode(evm.CALL).String() || opCode == evm.OpCode(evm.STATICCALL).String() {
			t.currentMemory = t.currentMemory[:len(t.currentMemory)-1]
		}

		memorySize = len(t.currentMemory[len(t.currentMemory)-1])
		memory = make([]byte, memorySize)
		copy(memory, t.currentMemory[len(t.currentMemory)-1])
	}

	if t.Config.EnableStack {
		if opCode == evm.OpCode(evm.CALL).String() || opCode == evm.OpCode(evm.STATICCALL).String() {
			t.currentStack = t.currentStack[:len(t.currentStack)-1]
		}

		stack = make([]*big.Int, len(t.currentStack[len(t.currentStack)-1]))
		for i, v := range t.currentStack[len(t.currentStack)-1] {
			stack[i] = new(big.Int).Set(v)
		}
	}

	if t.Config.EnableReturnData {
		returnData = make([]byte, len(lastReturnData))

		copy(returnData, lastReturnData)
	}

	if t.Config.EnableStorage {
		if opCode == evm.OpCode(evm.CALL).String() || opCode == evm.OpCode(evm.STATICCALL).String() {
			t.storage = t.storage[:len(t.storage)-1]
		}

		contractStorage, ok := t.storage[len(t.storage)-1][contractAddress]

		if ok {
			storage = make(map[types.Hash]types.Hash, len(contractStorage))

			for k, v := range contractStorage {
				storage[k] = v
			}
		}
	}

	log := StructLog{
		Pc:            ip,
		Op:            opCode,
		Gas:           availableGas,
		GasCost:       cost,
		Memory:        memory,
		MemorySize:    memorySize,
		Stack:         stack,
		ReturnData:    returnData,
		Storage:       storage,
		Depth:         depth,
		RefundCounter: host.GetRefund(),
		Err:           err,
	}

	t.logs = append(t.logs, log)

	// we've got the log from the final instruction of the contract just exited so keep hold of it
	// and prepend it to the logs of the calling contract before re-organizing the stack
	if t.justExited {
		t.justExited = false

		size := len(t.callStack)
		if size <= 1 {
			return
		}

		call := t.callStack[size-1]
		parentCall := t.callStack[size-2]
		call.index = len(parentCall.logs)

		call.logs = append([]StructLog{log}, call.logs...)

		t.callStack = t.callStack[:size-1]
		size -= 1

		//call.gasUsed = gasUsed
		t.callStack[size-1].callFrames = append(t.callStack[size-1].callFrames, call)
	} else {
		t.callStack[len(t.callStack)-1].logs = append(t.callStack[len(t.callStack)-1].logs, log)
	}
}

type StructTraceResult struct {
	Failed      bool           `json:"failed"`
	Gas         uint64         `json:"gas"`
	ReturnValue string         `json:"returnValue"`
	StructLogs  []StructLogRes `json:"structLogs"`
}

type StructLogRes struct {
	Pc            uint64            `json:"pc"`
	Op            string            `json:"op"`
	Gas           uint64            `json:"gas"`
	GasCost       uint64            `json:"gasCost"`
	Depth         int               `json:"depth"`
	Error         string            `json:"error,omitempty"`
	Stack         []string          `json:"stack"`
	Memory        []string          `json:"memory"`
	Storage       map[string]string `json:"storage"`
	RefundCounter uint64            `json:"refund,omitempty"`
}

func (t *StructTracer) GetResult() (interface{}, error) {
	if t.reason != nil {
		return nil, t.reason
	}

	var returnValue string

	if t.err != nil && !errors.Is(t.err, runtime.ErrExecutionReverted) {
		returnValue = ""
	} else {
		returnValue = fmt.Sprintf("%x", t.output)
	}

	return &StructTraceResult{
		Failed:      t.err != nil,
		Gas:         t.consumedGas,
		ReturnValue: returnValue,
		StructLogs:  formatStructLogs(t.logs, t.Config),
	}, nil
}

func formatStructLogs(originalLogs []StructLog, config Config) []StructLogRes {
	res := make([]StructLogRes, len(originalLogs))

	for index, log := range originalLogs {
		res[index] = StructLogRes{
			Pc:            log.Pc,
			Op:            log.Op,
			Gas:           log.Gas,
			GasCost:       log.GasCost,
			Depth:         log.Depth,
			Error:         log.ErrorString(),
			RefundCounter: log.RefundCounter,
		}

		res[index].Stack = make([]string, len(log.Stack))

		for i, value := range log.Stack {
			if config.Legacy {
				res[index].Stack[i] = hex.EncodeBigLegacy(value)
			} else {
				res[index].Stack[i] = hex.EncodeBig(value)
			}
		}

		res[index].Memory = make([]string, 0, (len(log.Memory)+31)/32)

		if log.Memory != nil {
			for i := 0; i+32 <= len(log.Memory); i += 32 {
				res[index].Memory = append(
					res[index].Memory,
					hex.EncodeToString(log.Memory[i:i+32]),
				)
			}
		}

		res[index].Storage = make(map[string]string)

		for key, value := range log.Storage {
			res[index].Storage[hex.EncodeToString(key.Bytes())] = hex.EncodeToString(value.Bytes())
		}
	}

	return res
}

func (t *StructTracer) GetLogsCsv() string {
	var output string

	for _, frame := range t.callStack {
		output += appendLogs(frame)
	}

	return output
}

func appendLogs(frame *callFrame) string {
	var out string
	for idx, l := range frame.logs {
		if l.Depth > 1 {
			out += fmt.Sprintf("%v,%s,%v\n", l.Pc, l.Op, l.GasCost)
		}

		// check if a sub call happened in any of the child frames
		for _, call := range frame.callFrames {
			if call.index == idx {
				out += appendLogs(call)
			}
		}

		if l.Depth == 1 {
			out += fmt.Sprintf("%v,%s,%v\n", l.Pc, l.Op, l.GasCost)
		}
	}

	return out
}
