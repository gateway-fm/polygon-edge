package runtime

import (
	"github.com/0xPolygon/polygon-edge/contracts"
	"github.com/0xPolygon/polygon-edge/types"
)

// for EIP-2929 we need to start an access list with the precompiled contracts so we hold a reference to
// it and build it once
var precompiledContracts []types.Address

func init() {
	// taken from the precompiled package be sure to update both
	precompiledContracts = []types.Address{
		types.StringToAddress("1"),
		types.StringToAddress("2"),
		types.StringToAddress("3"),
		types.StringToAddress("4"),
		types.StringToAddress("5"),
		types.StringToAddress("6"),
		types.StringToAddress("7"),
		types.StringToAddress("8"),
		types.StringToAddress("9"),
		types.StringToAddress(contracts.NativeTransferPrecompile.String()),
		types.StringToAddress(contracts.BLSAggSigsVerificationPrecompile.String()),
	}
}

type AccessList struct {
	addresses map[types.Address]int
	slots     []map[types.Hash]struct{}
}

// ContainsAddress returns true if the address is in the access list.
func (al *AccessList) ContainsAddress(address types.Address) bool {
	_, ok := al.addresses[address]
	return ok
}

// Contains checks if a slot within an account is present in the access list, returning
// separate flags for the presence of the account and the slot respectively.
func (al *AccessList) Contains(address types.Address, slot types.Hash) (addressPresent bool, slotPresent bool) {
	idx, ok := al.addresses[address]
	if !ok {
		// no such address (and hence zero slots)
		return false, false
	}
	if idx == -1 {
		// address yes, but no slots
		return true, false
	}
	_, slotPresent = al.slots[idx][slot]
	return true, slotPresent
}

// NewAccessList creates a new accessList.
func NewAccessList(init ...types.Address) *AccessList {
	al := &AccessList{
		addresses: make(map[types.Address]int),
	}

	for _, addr := range init {
		al.forceAddAddress(addr)
	}

	for _, addr := range precompiledContracts {
		al.forceAddAddress(addr)
	}

	return al
}

// Copy creates an independent copy of an accessList.
func (al *AccessList) Copy() *AccessList {
	cp := NewAccessList()
	for k, v := range al.addresses {
		cp.addresses[k] = v
	}
	cp.slots = make([]map[types.Hash]struct{}, len(al.slots))
	for i, slotMap := range al.slots {
		newSlotmap := make(map[types.Hash]struct{}, len(slotMap))
		for k := range slotMap {
			newSlotmap[k] = struct{}{}
		}
		cp.slots[i] = newSlotmap
	}
	return cp
}

// AddAddress adds an address to the access list, and returns 'true' if the operation
// caused a change (addr was not previously in the list).
func (al *AccessList) AddAddress(address types.Address) bool {
	if _, present := al.addresses[address]; present {
		return false
	}
	al.addresses[address] = -1
	return true
}

// forceAddAddress adds an address to the access list forcefully - used when adding precompiled contracts on creation
func (al *AccessList) forceAddAddress(address types.Address) {
	al.addresses[address] = -1
}

// AddSlot adds the specified (addr, slot) combo to the access list.
// Return values are:
// - address added
// - slot added
// For any 'true' value returned, a corresponding journal entry must be made.
func (al *AccessList) AddSlot(address types.Address, slot types.Hash) (addrChange bool, slotChange bool) {
	idx, addrPresent := al.addresses[address]
	if !addrPresent || idx == -1 {
		// Address not present, or addr present but no slots there
		al.addresses[address] = len(al.slots)
		slotmap := map[types.Hash]struct{}{slot: {}}
		al.slots = append(al.slots, slotmap)
		return !addrPresent, true
	}
	// There is already an (address,slot) mapping
	slotmap := al.slots[idx]
	if _, ok := slotmap[slot]; !ok {
		slotmap[slot] = struct{}{}
		// Journal add slot change
		return false, true
	}
	// No changes required
	return false, false
}

// DeleteSlot removes an (address, slot)-tuple from the access list.
// This operation needs to be performed in the same order as the addition happened.
// This method is meant to be used  by the journal, which maintains ordering of
// operations.
func (al *AccessList) DeleteSlot(address types.Address, slot types.Hash) {
	idx, addrOk := al.addresses[address]
	// There are two ways this can fail
	if !addrOk {
		panic("reverting slot change, address not present in list")
	}
	slotmap := al.slots[idx]
	delete(slotmap, slot)
	// If that was the last (first) slot, remove it
	// Since additions and rollbacks are always performed in order,
	// we can delete the item without worrying about screwing up later indices
	if len(slotmap) == 0 {
		al.slots = al.slots[:idx]
		al.addresses[address] = -1
	}
}

// DeleteAddress removes an address from the access list. This operation
// needs to be performed in the same order as the addition happened.
// This method is meant to be used  by the journal, which maintains ordering of
// operations.
func (al *AccessList) DeleteAddress(address types.Address) {
	delete(al.addresses, address)
}
