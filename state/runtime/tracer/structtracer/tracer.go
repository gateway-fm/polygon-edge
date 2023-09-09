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
	from       types.Address
	to         types.Address
	input      []byte
	value      *big.Int
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
		from:       from,
		to:         to,
		input:      input,
		value:      value,
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
	Error         string            `json:"error"`
	Stack         []string          `json:"stack,omitempty"`
	Memory        []string          `json:"memory,omitempty"`
	Storage       map[string]string `json:"storage,omitempty"`
	RefundCounter uint64            `json:"refund,omitempty"`
	Reason        *string           `json:"reason"`
	From          string            `json:"from,omitempty"`
	To            string            `json:"to,omitempty"`
	Input         string            `json:"input,omitempty"`
	Value         string            `json:"value,omitempty"`
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

	logs := make([]StructLogRes, 0)
	for _, frame := range t.callStack {
		logs = append(logs, appendStructLogs(frame, t.Config)...)
	}

	return &StructTraceResult{
		Failed:      t.err != nil,
		Gas:         t.consumedGas,
		ReturnValue: returnValue,
		StructLogs:  logs,
	}, nil
}

func (t *StructTracer) GetResultLegacy() (interface{}, error) {
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
		res[index] = structLogToRes(log, config)
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

func appendStructLogs(frame *callFrame, config Config) []StructLogRes {
	var out []StructLogRes
	for idx, l := range frame.logs {
		toAppend := structLogToRes(l, config)

		checkForCall(frame, &toAppend)

		if l.Depth > 1 {
			out = append(out, toAppend)
		}

		// check if a sub call happened in any of the child frames
		for _, call := range frame.callFrames {
			if call.index == idx {
				out = append(out, appendStructLogs(call, config)...)
			}
		}

		if l.Depth == 1 {
			out = append(out, toAppend)
		}
	}

	return out
}

func checkForCall(frame *callFrame, toAppend *StructLogRes) {
	if toAppend.Op == "CALL" ||
		toAppend.Op == "STATICCALL" ||
		toAppend.Op == "DELEGATECALL" ||
		toAppend.Op == "CALLCODE" {
		gas := calculateCallGas(frame, true)
		toAppend.GasCost -= gas
		toAppend.From = frame.from.String()
		toAppend.To = frame.to.String()
		toAppend.Input = "0x" + hex.EncodeToString(frame.input)
		if frame.value != nil {
			toAppend.Value = hex.EncodeBig(frame.value)
		} else {
			toAppend.Value = "0x0"
		}
	}
}

func calculateCallGas(frame *callFrame, topLevel bool) uint64 {
	var gasUsed uint64
	for idx, l := range frame.logs {

		if l.Op == "CALL" || l.Op == "STATICCALL" || l.Op == "DELEGATECALL" || l.Op == "CALLCODE" {
			if topLevel {
				continue
			}
			// we're at the start of a subcall so take the cost and minus the subcall costs
			cost := l.GasCost
			log := frame.logs[0]
			frame.logs = frame.logs[1:]
			cost -= calculateCallGas(frame, false)
			frame.logs = append([]StructLog{log}, frame.logs...)
			gasUsed += cost
		} else {
			gasUsed += l.GasCost
		}

		for _, call := range frame.callFrames {
			if call.index == idx {
				gasUsed += calculateCallGas(call, false)
			}
		}
	}

	return gasUsed
}

func structLogToRes(l StructLog, config Config) StructLogRes {
	toAppend := StructLogRes{
		Pc:            l.Pc,
		Op:            l.Op,
		Gas:           l.Gas,
		GasCost:       l.GasCost,
		Depth:         l.Depth,
		Error:         l.ErrorString(),
		RefundCounter: l.RefundCounter,
	}

	toAppend.Stack = make([]string, len(l.Stack))
	if config.EnableStack {
		for i, value := range l.Stack {
			if config.Legacy {
				toAppend.Stack[i] = hex.EncodeBigLongFormat(value)
			} else {
				toAppend.Stack[i] = hex.EncodeBig(value)
			}
		}
	}

	toAppend.Memory = make([]string, 0, (len(l.Memory)+31)/32)
	if config.EnableMemory && l.Memory != nil {
		for i := 0; i+32 <= len(l.Memory); i += 32 {
			toAppend.Memory = append(
				toAppend.Memory,
				hex.EncodeToString(l.Memory[i:i+32]),
			)
		}
	}

	toAppend.Storage = make(map[string]string)
	if config.EnableStorage {
		for key, value := range l.Storage {
			toAppend.Storage[hex.EncodeToString(key.Bytes())] = hex.EncodeToString(value.Bytes())
		}
	}

	return toAppend
}
