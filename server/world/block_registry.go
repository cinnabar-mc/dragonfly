package world

import (
	"fmt"
	"maps"
	"math"
	"math/bits"
	"reflect"
	"slices"
	"sync"

	"github.com/brentp/intintmap"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/df-mc/worldupgrader/blockupgrader"
	"github.com/segmentio/fasthash/fnv1"
)

// DefaultBlockRegistry is the default (vanilla) block registry used by Dragonfly when no custom registry is provided.
//
// The registry is populated during package init (block states + block implementations) and is finalized on first use
// by `server.Config.New()`, `world.Config.New()`, or `mcdb.Config.Open()`. Callers that need custom blocks should
// create a new registry using `NewBlockRegistry()` and pass it through config instead of mutating this value.
var DefaultBlockRegistry = &BasicBlockRegistry{
	blockProperties: make(map[string]map[string]any),
	stateRuntimeIDs: make(map[stateHash]uint32),
	customBlocks:    make(map[string]CustomBlock),
}

// BlockRegistry converts between runtime IDs and block states/implementations.
//
// A BlockRegistry has a build/finalize lifecycle:
//   - During setup, blocks and block states may be registered using RegisterBlockState/RegisterBlock.
//   - After calling Finalize, the registry becomes immutable and is ready for use in chunk encoding/decoding and
//     network serialization. RegisterBlock/RegisterBlockState will panic after finalization.
//
// The interface is split because the chunk package cannot import world.Block.
type BlockRegistry interface {
	chunk.BlockRegistry

	// BlockByRuntimeID looks up a Block by runtime ID. If the runtime ID is unknown/out of range, ok is false.
	BlockByRuntimeID(rid uint32) (Block, bool)
	// BlockByRuntimeIDOrAir looks up a Block by runtime ID. If not found, an air block is returned.
	BlockByRuntimeIDOrAir(rid uint32) Block
	// BlockRuntimeID looks up the runtime ID of a previously registered Block.
	BlockRuntimeID(block Block) (rid uint32)
	// RegisterBlock registers a Block implementation for a previously registered
	// state, or collects actor and item metadata without installing behaviour.
	RegisterBlock(block BlockType)
	// RegisterBlockState registers a block state that blocks may encode to.
	RegisterBlockState(blockState BlockState)
	// CustomBlocks returns custom blocks registered in this registry, keyed by identifier.
	CustomBlocks() map[string]CustomBlock
	// BlockByName looks up a Block by full identifier and properties.
	BlockByName(name string, properties map[string]any) (Block, bool)
	// Blocks returns all blocks registered in the registry, indexed by runtime ID.
	Blocks() []Block
	// BlockActorID returns the NBT block-actor ID for a block identifier. Metadata
	// alone does not make an unimplemented block an NBTBlock.
	BlockActorID(name string) (string, bool)
	// BlockActorIDs returns an independent copy of all block-actor metadata,
	// including aliases that do not have registered block states.
	BlockActorIDs() map[string]string
	// Air returns the air block registered in the registry.
	Air() Block
	// Finalize finalizes the registry, building derived lookup tables required for runtime usage. Finalize is
	// idempotent.
	Finalize()
	// BitSize returns the number of bits used by BlockHash (depends on the number of registered blocks).
	BitSize() int
	// BlockHash returns a unique identifier of the block including the block states. The hash is internal to Dragonfly
	// and is used for fast map lookups; it does not need to match any in-game identifiers.
	BlockHash(b Block) uint64
	// RuntimeIDToHash resolves a runtime ID to its network block hash.
	RuntimeIDToHash(runtimeID uint32) (hash uint32, ok bool)
}

// BlockStateResolver resolves versioned block states read from NBT. Registries
// that support tolerant input and historical state upgrades may implement this
// interface without expanding the core BlockRegistry contract.
type BlockStateResolver interface {
	ResolveBlockState(state BlockState) (Block, bool)
}

// ResolveBlockState upgrades and resolves a versioned block state using registry.
// Registries may implement BlockStateResolver to accept loosely typed or otherwise
// non-canonical state data. Other registries receive the upgraded state unchanged.
func ResolveBlockState(registry BlockRegistry, state BlockState) (Block, bool) {
	if resolver, ok := registry.(BlockStateResolver); ok {
		return resolver.ResolveBlockState(state)
	}
	properties := maps.Clone(state.Properties)
	if properties == nil {
		properties = make(map[string]any)
	}
	if !ValidBlockPropertyValues(properties) {
		return nil, false
	}
	upgraded := blockupgrader.Upgrade(blockupgrader.BlockState{
		Name: state.Name, Properties: properties, Version: state.Version,
	})
	return registry.BlockByName(upgraded.Name, upgraded.Properties)
}

const (
	blockFlagNBT uint16 = 1 << iota
	blockFlagRandomTick
	blockFlagLiquid
	blockFlagLiquidDisplacing
)

// blockInfo is a packed set of per-runtime-ID properties used in hot paths (lighting, ticking, fluid checks, etc.).
//
// Layout (uint16):
// - bits 0..3: boolean flags (blockFlag*).
// - bits 8..11: 4-bit light emission level (0-15).
// - bits 12..15: 4-bit light filtering level (0-15).
//
// Values are computed during BlockRegistry.Finalize() and stored in BasicBlockRegistry.blockInfos, indexed by runtime ID.
type blockInfo uint16

func (b *blockInfo) set(flag uint16) {
	*b |= blockInfo(flag)
}

func (b blockInfo) get(flag uint16) bool {
	return uint16(b)&flag != 0
}

func (b *blockInfo) setLight(light uint8) {
	// Overwrite the 4-bit light emission field.
	*b &^= blockInfo(0xF) << 8
	*b |= blockInfo(light&0xF) << 8
}

func (b *blockInfo) setLightFilter(light uint8) {
	// Overwrite the 4-bit light filtering field.
	*b &^= blockInfo(0xF) << 12
	*b |= blockInfo(light&0xF) << 12
}

func (b blockInfo) getLight() uint8 {
	return uint8((b >> 8) & 0xF)
}

func (b blockInfo) getLightFilter() uint8 {
	return uint8((b >> 12) & 0xF)
}

// BasicBlockRegistry is the default BlockRegistry implementation used by Dragonfly.
type BasicBlockRegistry struct {
	mu sync.Mutex

	finalized         bool
	bitSize           int
	hashes            *intintmap.Map
	networkhashToRids map[uint32]uint32
	ridsToNetworkhash []uint32
	// hashedNetworkIDs is set by UseHashedNetworkIDs: every state's network block hash must then be unique.
	hashedNetworkIDs bool
	// networkHashSalts holds, by block name, the NetworkHashSaltProperty value of each custom block whose network
	// block hashes Finalize perturbed.
	networkHashSalts map[string]int32

	// stateRuntimeIDs holds a map for looking up the runtime ID of a block by the stateHash it produces.
	stateRuntimeIDs map[stateHash]uint32

	blockProperties map[string]map[string]any
	// blocks holds a list of all registered Blocks indexed by their runtime ID. Blocks that were not explicitly
	// registered are of the type unknownBlock.
	blocks []Block
	// customBlocks maps a custom block's identifier to the custom block.
	customBlocks map[string]CustomBlock
	// blockActorIDs is derived from registered types' BlockActorID methods and aliases.
	blockActorIDs map[string]string

	blockInfos []blockInfo

	airRID uint32
}

func (br *BasicBlockRegistry) BitSize() int {
	if !br.finalized {
		panic("BlockRegistry.BitSize called on non finalized BlockRegistry")
	}
	return br.bitSize
}

func (br *BasicBlockRegistry) BlockCount() int {
	if !br.finalized {
		panic("BlockRegistry.BlockCount called on non finalized BlockRegistry")
	}
	return len(br.blockInfos)
}

// blockInfoOrDefault returns the blockInfo for rid, or the default unknown-block info when rid is out of range. Chunk
// decoding preserves unknown network hashes as raw palette values, so out-of-range runtime IDs can legitimately reach
// these lookups.
func (br *BasicBlockRegistry) blockInfoOrDefault(rid uint32) blockInfo {
	if rid >= uint32(len(br.blockInfos)) {
		return defaultUnknownBlockInfo()
	}
	return br.blockInfos[rid]
}

func (br *BasicBlockRegistry) RandomTickBlock(rid uint32) bool {
	if !br.finalized {
		panic("BlockRegistry.RandomTickBlock called on non finalized BlockRegistry")
	}
	return br.blockInfoOrDefault(rid).get(blockFlagRandomTick)
}

func (br *BasicBlockRegistry) FilteringBlock(rid uint32) uint8 {
	if !br.finalized {
		panic("BlockRegistry.FilteringBlock called on non finalized BlockRegistry")
	}
	return br.blockInfoOrDefault(rid).getLightFilter()
}

func (br *BasicBlockRegistry) LightBlock(rid uint32) uint8 {
	if !br.finalized {
		panic("BlockRegistry.LightBlock called on non finalized BlockRegistry")
	}
	return br.blockInfoOrDefault(rid).getLight()
}

func (br *BasicBlockRegistry) NBTBlock(rid uint32) bool {
	if !br.finalized {
		panic("BlockRegistry.NBTBlock called on non finalized BlockRegistry")
	}
	return br.blockInfoOrDefault(rid).get(blockFlagNBT)
}

func (br *BasicBlockRegistry) LiquidDisplacingBlock(rid uint32) bool {
	if !br.finalized {
		panic("BlockRegistry.LiquidDisplacingBlock called on non finalized BlockRegistry")
	}
	return br.blockInfoOrDefault(rid).get(blockFlagLiquidDisplacing)
}

func (br *BasicBlockRegistry) LiquidBlock(rid uint32) bool {
	if !br.finalized {
		panic("BlockRegistry.LiquidBlock called on non finalized BlockRegistry")
	}
	return br.blockInfoOrDefault(rid).get(blockFlagLiquid)
}

func (br *BasicBlockRegistry) Blocks() []Block {
	if !br.finalized {
		panic("BlockRegistry.Blocks called on non finalized BlockRegistry")
	}
	return slices.Clone(br.blocks)
}

// BlockActorID returns the NBT block-actor ID registered for name.
func (br *BasicBlockRegistry) BlockActorID(name string) (string, bool) {
	if !br.finalized {
		panic("BlockRegistry.BlockActorID called on non finalized BlockRegistry")
	}
	id, ok := br.blockActorIDs[name]
	return id, ok
}

// BlockActorIDs returns an independent copy of the registered actor IDs.
func (br *BasicBlockRegistry) BlockActorIDs() map[string]string {
	if !br.finalized {
		panic("BlockRegistry.BlockActorIDs called on non finalized BlockRegistry")
	}
	return maps.Clone(br.blockActorIDs)
}

// validateBlockActorLocked checks metadata against existing registrations while br.mu is held.
func (br *BasicBlockRegistry) validateBlockActorLocked(name, id string) {
	if name == "" || id == "" {
		panic("block actor name and ID must not be empty")
	}
	if existing, ok := br.blockActorIDs[name]; ok && existing != id {
		panic(fmt.Sprintf("conflicting block actor IDs for %s: %s and %s", name, existing, id))
	}
}

// registerBlockActorLocked adds consistent metadata while br.mu is held.
func (br *BasicBlockRegistry) registerBlockActorLocked(name, id string) {
	br.validateBlockActorLocked(name, id)
	if br.blockActorIDs == nil {
		br.blockActorIDs = make(map[string]string)
	}
	br.blockActorIDs[name] = id
}

// UseHashedNetworkIDs declares that the registry's states go to clients as network block hashes, as they do for a
// server that sets StartGame's UseBlockNetworkIDHashes. Finalize then gives every state a unique hash: a custom block
// with a state whose hash an earlier state already has gets the hidden NetworkHashSaltProperty (see NetworkHashSalt),
// and a collision between two states that are not custom blocks panics, naming both. Without it, states may share a
// hash, and HashToRuntimeID resolves a shared hash to the lowest of their runtime IDs.
//
// UseHashedNetworkIDs panics on a finalized registry.
func (br *BasicBlockRegistry) UseHashedNetworkIDs() {
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.finalized {
		panic("BlockRegistry.UseHashedNetworkIDs called on finalized BlockRegistry")
	}
	br.hashedNetworkIDs = true
}

// NetworkHashSalt returns the NetworkHashSaltProperty value that Finalize gave the custom block name to keep its
// network block hashes unique, if it gave it one. A client hashes a custom block's states from its definition, so the
// definition sent to it must declare the property with this one value (customentry.DeclareNetworkHashSalt). The
// property is in no state the block encodes: it is never saved and never reaches Block implementations.
func (br *BasicBlockRegistry) NetworkHashSalt(name string) (salt int32, ok bool) {
	if !br.finalized {
		panic("BlockRegistry.NetworkHashSalt called on non finalized BlockRegistry")
	}
	salt, ok = br.networkHashSalts[name]
	return
}

// HashToRuntimeID resolves a network block hash to the lowest runtime ID of a state with that hash.
func (br *BasicBlockRegistry) HashToRuntimeID(hash uint32) (rid uint32, ok bool) {
	if !br.finalized {
		panic("BlockRegistry.HashToRuntimeID called on non finalized BlockRegistry")
	}
	rid, ok = br.networkhashToRids[hash]
	return
}

func (br *BasicBlockRegistry) RuntimeIDToHash(runtimeID uint32) (hash uint32, ok bool) {
	if !br.finalized {
		panic("BlockRegistry.RuntimeIDToHash called on non finalized BlockRegistry")
	}
	if runtimeID >= uint32(len(br.ridsToNetworkhash)) {
		return 0, false
	}
	return br.ridsToNetworkhash[runtimeID], true
}

// Clone returns an independent copy of the registry. If the source registry is finalized, the clone is also finalized.
// If the source is not finalized, the clone remains mutable.
func (br *BasicBlockRegistry) Clone() *BasicBlockRegistry {
	br.mu.Lock()
	defer br.mu.Unlock()

	br2 := &BasicBlockRegistry{
		blockProperties:  make(map[string]map[string]any, len(br.blockProperties)),
		stateRuntimeIDs:  make(map[stateHash]uint32, len(br.stateRuntimeIDs)),
		customBlocks:     make(map[string]CustomBlock, len(br.customBlocks)),
		blockActorIDs:    maps.Clone(br.blockActorIDs),
		finalized:        br.finalized,
		bitSize:          br.bitSize,
		airRID:           br.airRID,
		hashedNetworkIDs: br.hashedNetworkIDs,
		networkHashSalts: maps.Clone(br.networkHashSalts),
	}

	for k, v := range br.blockProperties {
		br2.blockProperties[k] = maps.Clone(v)
	}
	maps.Copy(br2.stateRuntimeIDs, br.stateRuntimeIDs)
	maps.Copy(br2.customBlocks, br.customBlocks)

	br2.blocks = make([]Block, len(br.blocks))
	copy(br2.blocks, br.blocks)
	br2.blockInfos = append([]blockInfo(nil), br.blockInfos...)

	if br.finalized {
		br2.rebuildBlockHashesLocked()
		br2.networkhashToRids = make(map[uint32]uint32, len(br.networkhashToRids))
		maps.Copy(br2.networkhashToRids, br.networkhashToRids)
		br2.ridsToNetworkhash = append([]uint32(nil), br.ridsToNetworkhash...)
	}

	return br2
}

func (br *BasicBlockRegistry) addCustomBlockState(s BlockState, scratch []byte) ([]byte, error) {
	br.mu.Lock()
	defer br.mu.Unlock()

	h := stateHash{name: s.Name, properties: hashProperties(s.Properties)}
	if _, ok := br.stateRuntimeIDs[h]; ok {
		return scratch, nil
	}
	var netHash uint32
	if br.finalized {
		netHash, scratch = networkBlockHash(s.Name, s.Properties, scratch)
		if other, ok := br.networkhashToRids[netHash]; ok && br.hashedNetworkIDs {
			otherName, otherProperties := br.blocks[other].EncodeBlock()
			return scratch, fmt.Errorf("network block hash %#08x of (%s %+v) is already (%s %+v)'s, and runtime IDs go to clients as network block hashes", netHash, s.Name, s.Properties, otherName, otherProperties)
		}
	}
	if _, ok := br.blockProperties[s.Name]; !ok {
		br.blockProperties[s.Name] = s.Properties
	}

	rid := uint32(len(br.blocks))
	br.blocks = append(br.blocks, unknownBlock{BlockState: s})
	br.stateRuntimeIDs[h] = rid
	if !br.finalized {
		return scratch, nil
	}

	if bits.Len64(uint64(len(br.blocks))) != br.bitSize {
		br.bitSize = bits.Len64(uint64(len(br.blocks)))
		br.rebuildBlockHashesLocked()
	}
	br.blockInfos = append(br.blockInfos, defaultUnknownBlockInfo())

	if _, ok := br.networkhashToRids[netHash]; !ok {
		br.networkhashToRids[netHash] = rid
	}
	br.ridsToNetworkhash = append(br.ridsToNetworkhash, netHash)
	return scratch, nil
}

func (br *BasicBlockRegistry) rebuildBlockHashesLocked() {
	br.hashes = intintmap.New(len(br.blocks), 0.999)
	for rid, b := range br.blocks {
		if _, hash := b.Hash(); hash == math.MaxUint64 {
			continue
		}
		br.hashes.Put(int64(br.BlockHash(b)), int64(rid))
	}
}

func defaultUnknownBlockInfo() blockInfo {
	var info blockInfo
	info.setLightFilter(15)
	return info
}

// NewBlockRegistry returns a mutable registry seeded with all vanilla block states and block implementations.
// Callers may RegisterBlockState/RegisterBlock and must call Finalize() before using the registry for world/chunk
// serialization. The returned registry is independent from DefaultBlockRegistry.
func NewBlockRegistry() BlockRegistry {
	// Clone() produces a fully populated registry. We then mark it mutable again so that additional block states and
	// blocks can be registered before finalizing.
	br := DefaultBlockRegistry.Clone()
	br.finalized = false
	br.bitSize = 0
	br.hashes = nil
	br.networkhashToRids = nil
	br.ridsToNetworkhash = nil
	br.networkHashSalts = nil
	br.blockInfos = nil
	return br
}

// RegisterBlock registers a block's behaviour, actor and placement-item metadata.
// A type implementing Item without Block contributes inventory semantics for its
// exact state without installing gameplay behaviour.
// RegisterBlock panics on invalid states, traits or conflicting actor metadata.
func (br *BasicBlockRegistry) RegisterBlock(b BlockType) {
	br.mu.Lock()
	defer br.mu.Unlock()

	if br.finalized {
		panic("BlockRegistry.RegisterBlock called on finalized BlockRegistry")
	}
	name, properties := b.EncodeBlock()
	implementation, implemented := b.(Block)
	actor, hasActor := b.(BlockActor)
	placementItem, hasItem := b.(Item)
	if !implemented && !hasActor && !hasItem {
		panic(fmt.Sprintf("block type %s has neither behaviour nor metadata", name))
	}
	var actorNames []string
	var actorID string
	if hasActor {
		actorNames, actorID = []string{name}, actor.BlockActorID()
		if aliases, ok := b.(interface{ BlockActorAliases() []string }); ok {
			actorNames = append(actorNames, aliases.BlockActorAliases()...)
		}
		for _, actorName := range actorNames {
			br.validateBlockActorLocked(actorName, actorID)
		}
	}
	if implemented {
		c, custom := b.(CustomBlock)
		_, known := br.customBlocks[name]
		if custom && !known {
			if err := validateCustomBlock(c); err != nil {
				panic(fmt.Sprintf("custom block %s: %v", name, err))
			}
		}
		if custom {
			br.registerBlockStateLocked(BlockState{Name: name, Properties: properties})
		}
		rid, ok := br.stateRuntimeIDs[stateHash{name: name, properties: hashProperties(properties)}]
		if !ok {
			// We assume all blocks must have all their states registered beforehand. Vanilla blocks will have
			// this done through registering of all states present in the block_states.nbt file.
			panic(fmt.Sprintf("block state returned is not registered (%v {%#v})", name, properties))
		}
		if _, ok := br.blocks[rid].(unknownBlock); !ok {
			panic(fmt.Sprintf("block with name and properties %v {%#v} already registered", name, properties))
		}
		br.blocks[rid] = implementation
		if custom && !known {
			br.customBlocks[name] = c
		}
	}
	if !implemented && hasItem {
		rid, ok := br.stateRuntimeIDs[stateHash{name: name, properties: hashProperties(properties)}]
		if !ok {
			panic(fmt.Sprintf("placement-item state is not registered (%s {%#v})", name, properties))
		}
		unknown, ok := br.blocks[rid].(unknownBlock)
		if !ok || unknown.placementItem != nil {
			panic(fmt.Sprintf("placement item already registered for %s {%#v}", name, properties))
		}
		unknown.placementItem = placementItem
		br.blocks[rid] = unknown
	}
	for _, actorName := range actorNames {
		br.registerBlockActorLocked(actorName, actorID)
	}
}

// RegisterBlockState registers a BlockState to the registry. The function panics if the properties the
// BlockState holds are invalid or if the BlockState was already registered.
func (br *BasicBlockRegistry) RegisterBlockState(s BlockState) {
	br.mu.Lock()
	defer br.mu.Unlock()
	br.registerBlockStateLocked(s)
}

// registerBlockStateLocked is the implementation of RegisterBlockState. br.mu must be held by the caller.
func (br *BasicBlockRegistry) registerBlockStateLocked(s BlockState) {
	if br.finalized {
		panic("BlockRegistry.RegisterBlockState called on finalized BlockRegistry")
	}
	h := stateHash{name: s.Name, properties: hashProperties(s.Properties)}
	if _, ok := br.stateRuntimeIDs[h]; ok {
		panic(fmt.Sprintf("cannot register the same state twice (%+v)", s))
	}
	if _, ok := br.blockProperties[s.Name]; !ok {
		br.blockProperties[s.Name] = s.Properties
	}
	rid := uint32(len(br.blocks))
	br.blocks = append(br.blocks, unknownBlock{BlockState: s})
	br.stateRuntimeIDs[h] = rid
}

func (br *BasicBlockRegistry) Finalize() {
	br.mu.Lock()
	defer br.mu.Unlock()

	if br.finalized {
		// Finalize is intentionally idempotent so code can defensively call it when a registry is provided through
		// config. RegisterBlock/RegisterBlockState still panic after finalization.
		return
	}

	br.sortForNetworkLocked()

	br.finalizeLocked()
}

// finalizeLocked builds lookup tables without changing block order. The registry
// lock must be held; snapshot restore uses this to preserve recorded runtime IDs.
func (br *BasicBlockRegistry) finalizeLocked() {
	br.bitSize = bits.Len64(uint64(len(br.blocks)))
	br.blockInfos = make([]blockInfo, len(br.blocks))
	br.hashes = intintmap.New(len(br.blocks), 0.999)
	br.ridsToNetworkhash = make([]uint32, len(br.blocks))
	br.stateRuntimeIDs = make(map[stateHash]uint32, len(br.blocks))
	networkHashScratch := make([]byte, 0, 0xff)
	foundAir := false

	for idx, b := range br.blocks {
		rid := uint32(idx)
		name, properties := b.EncodeBlock()
		h := stateHash{name: name, properties: hashProperties(properties)}
		if name == "minecraft:air" {
			br.airRID = rid
			foundAir = true
		}
		if _, ok := br.stateRuntimeIDs[h]; ok {
			panic(fmt.Sprintf("cannot register the same state twice (%s %+v)", name, properties))
		}
		br.stateRuntimeIDs[h] = rid

		info := defaultUnknownBlockInfo()
		// Default to fully opaque. Blocks that implement lightDiffuser may override this (e.g., air -> 0, leaves -> 1-14).
		if diffuser, ok := b.(lightDiffuser); ok {
			info.setLightFilter(diffuser.LightDiffusionLevel())
		}
		if emitter, ok := b.(lightEmitter); ok {
			info.setLight(emitter.LightEmissionLevel())
		}
		if _, ok := b.(NBTer); ok {
			info.set(blockFlagNBT)
		}
		if _, ok := b.(RandomTicker); ok {
			info.set(blockFlagRandomTick)
		}
		if _, ok := b.(Liquid); ok {
			info.set(blockFlagLiquid)
		}
		if _, ok := b.(LiquidDisplacer); ok {
			info.set(blockFlagLiquidDisplacing)
		}
		br.blockInfos[rid] = info

		if _, hash := b.Hash(); hash != math.MaxUint64 {
			h := int64(br.BlockHash(b))
			if other, ok := br.hashes.Get(h); ok {
				panic(fmt.Sprintf("block %#v with hash %v already registered by %#v", b, h, br.blocks[other]))
			}
			br.hashes.Put(h, int64(rid))
		}
		br.ridsToNetworkhash[rid], networkHashScratch = networkBlockHash(name, properties, networkHashScratch)
	}
	if err := br.indexNetworkHashesLocked(); err != nil {
		panic(err)
	}
	if !foundAir {
		panic("BlockRegistry.Finalize: no minecraft:air block state registered")
	}
	br.finalized = true
}

// AirRuntimeID returns the runtime ID of the air block.
func (br *BasicBlockRegistry) AirRuntimeID() uint32 {
	if !br.finalized {
		panic("BlockRegistry.AirRuntimeID called on non finalized BlockRegistry")
	}
	return br.airRID
}

// RuntimeIDToState returns the name and state properties of a block by its runtime ID.
func (br *BasicBlockRegistry) RuntimeIDToState(runtimeID uint32) (name string, properties map[string]any, found bool) {
	if !br.finalized {
		panic("BlockRegistry.RuntimeIDToState called on non finalized BlockRegistry")
	}
	if runtimeID >= uint32(len(br.blocks)) {
		return "", nil, false
	}
	name, properties = br.blocks[runtimeID].EncodeBlock()
	return name, properties, true
}

// StateToRuntimeID returns the runtime ID of a block by its name and state properties.
func (br *BasicBlockRegistry) StateToRuntimeID(name string, properties map[string]any) (runtimeID uint32, found bool) {
	if !br.finalized {
		panic("BlockRegistry.StateToRuntimeID called on non finalized BlockRegistry")
	}
	if rid, ok := br.stateRuntimeIDs[stateHash{name: name, properties: hashProperties(properties)}]; ok {
		return rid, true
	}
	rid, ok := br.stateRuntimeIDs[stateHash{name: name, properties: hashProperties(br.blockProperties[name])}]
	return rid, ok
}

// DefaultProperties returns the properties of the default state of the block
// with the name passed. The palette does not mark a default, so the first state
// registered for the name is used. For vanilla blocks this matches the game's
// default for every property a stored state can lack, as properties older than
// the block state version are filled in by the upgrade schemas instead.
func (br *BasicBlockRegistry) DefaultProperties(name string) (map[string]any, bool) {
	if !br.finalized {
		panic("BlockRegistry.DefaultProperties called on non finalized BlockRegistry")
	}
	properties, ok := br.blockProperties[name]
	return properties, ok
}

// BlockHash returns a unique identifier of the block including the block states. This function is used internally
// to convert a block to a single integer which can be used in map lookups. The hash produced therefore does not
// need to match anything in the game, but it must be unique among all registered blocks.
// The tool in `/cmd/blockhash` may be used to automatically generate block hashes of blocks in a package.
func (br *BasicBlockRegistry) BlockHash(b Block) uint64 {
	base, hash := b.Hash()
	return base | (hash << uint64(br.bitSize))
}

// BlockRuntimeID attempts to return a runtime ID of a block previously registered using RegisterBlock().
// If the runtime ID cannot be found because the Block wasn't registered, BlockRuntimeID will panic.
func (br *BasicBlockRegistry) BlockRuntimeID(b Block) uint32 {
	if !br.finalized {
		panic("BlockRegistry.BlockRuntimeID called on non finalized BlockRegistry")
	}
	if b == nil {
		return br.airRID
	}
	if _, hash := b.Hash(); hash != math.MaxUint64 {
		// b is not an unknownBlock.
		if rid, ok := br.hashes.Get(int64(br.BlockHash(b))); ok {
			return uint32(rid)
		}
		panic(fmt.Sprintf("cannot find block by non-0 hash of block %#v", b))
	}
	return br.slowBlockRuntimeID(b)
}

func (br *BasicBlockRegistry) BlockByRuntimeIDOrAir(rid uint32) Block {
	bl, _ := br.BlockByRuntimeID(rid)
	return bl
}

// slowBlockRuntimeID finds the runtime ID of a Block by hashing the properties produced by calling the
// Block.EncodeBlock method and looking it up in the stateRuntimeIDs map.
func (br *BasicBlockRegistry) slowBlockRuntimeID(b Block) uint32 {
	name, properties := b.EncodeBlock()
	rid, ok := br.stateRuntimeIDs[stateHash{name: name, properties: hashProperties(properties)}]
	if !ok {
		panic(fmt.Sprintf("cannot find block by (name + properties): %#v", b))
	}
	return rid
}

// BlockByRuntimeID attempts to return a Block by its runtime ID. If not found, the bool returned is
// false. If found, the block is non-nil and the bool true.
func (br *BasicBlockRegistry) BlockByRuntimeID(rid uint32) (Block, bool) {
	if !br.finalized {
		panic("BlockRegistry.BlockByRuntimeID called on non finalized BlockRegistry")
	}
	if rid >= uint32(len(br.blocks)) {
		return br.Air(), false
	}
	return br.blocks[rid], true
}

// BlockByName attempts to return a Block by its name and properties. If not found, the bool returned is
// false.
func (br *BasicBlockRegistry) BlockByName(name string, properties map[string]any) (Block, bool) {
	if !br.finalized {
		panic("BlockRegistry.BlockByName called on non finalized BlockRegistry")
	}
	if !ValidBlockPropertyValues(properties) {
		return nil, false
	}
	rid, ok := br.stateRuntimeIDs[stateHash{name: name, properties: hashProperties(properties)}]
	if !ok {
		return nil, false
	}
	return br.blocks[rid], true
}

// ValidBlockPropertyValues reports whether properties contain only the scalar
// types a Bedrock block palette can hold, which is what the block upgrader and
// the registry expect to be handed.
func ValidBlockPropertyValues(properties map[string]any) bool {
	for _, value := range properties {
		if !validBlockPropertyValue(value) {
			return false
		}
	}
	return true
}

// validBlockPropertyValue reports whether value is a scalar supported by Bedrock block palettes.
func validBlockPropertyValue(value any) bool {
	switch value.(type) {
	case bool, uint8, int32, string:
		return true
	default:
		return false
	}
}

// ResolveBlockState upgrades and resolves a versioned block state decoded from NBT.
//
// Property values are hashed by type, so a boolean the palette holds as a byte
// does not match the same property written as an int. Structure files and other
// third-party sources do write them that way, and the resulting state would
// otherwise silently fail to resolve. Values are coerced to the type the
// palette declares for the block before the lookup, which leaves genuinely
// numeric properties such as a stair's direction alone. Properties the palette
// does not declare at all are dropped, since no registered state carries them.
func (br *BasicBlockRegistry) ResolveBlockState(state BlockState) (Block, bool) {
	properties := maps.Clone(state.Properties)
	if properties == nil {
		properties = make(map[string]any)
	}
	declared, knownName := br.blockProperties[state.Name]
	for property, value := range properties {
		if validBlockPropertyValue(value) {
			continue
		}
		_, declaredProperty := declared[property]
		if !knownName || declaredProperty || state.Version < chunk.CurrentBlockVersion {
			return nil, false
		}
		delete(properties, property)
	}
	match, cost, matched := br.resolveBlockStateCandidate(BlockState{Name: state.Name, Properties: properties, Version: state.Version})
	if matched && cost == 0 {
		return match, true
	}
	keys := looseNumericCandidateProperties(properties)
	if len(keys) == 0 {
		return match, matched
	}
	const maxLooseNumericProperties = 8
	if len(keys) > maxLooseNumericProperties {
		return nil, false
	}
	var matchState stateHash
	if matched {
		name, resolvedProperties := match.EncodeBlock()
		matchState = stateHash{name: name, properties: hashProperties(resolvedProperties)}
	}
	for mask := 1; mask < 1<<len(keys); mask++ {
		candidate := maps.Clone(properties)
		for index, key := range keys {
			if mask&(1<<index) != 0 {
				candidate[key] = alternateNumericEncoding(candidate[key])
			}
		}
		b, candidateCost, ok := br.resolveBlockStateCandidate(BlockState{Name: state.Name, Properties: candidate, Version: state.Version})
		if !ok || matched && candidateCost > cost {
			continue
		}
		name, resolvedProperties := b.EncodeBlock()
		resolvedState := stateHash{name: name, properties: hashProperties(resolvedProperties)}
		if !matched || candidateCost < cost {
			match, matchState, cost, matched = b, resolvedState, candidateCost, true
			continue
		}
		if resolvedState != matchState {
			return nil, false
		}
	}
	return match, matched
}

// resolveBlockStateCandidate upgrades one type interpretation and reports its normalization cost.
func (br *BasicBlockRegistry) resolveBlockStateCandidate(state BlockState) (Block, int, bool) {
	upgraded := blockupgrader.Upgrade(blockupgrader.BlockState{
		Name: state.Name, Properties: maps.Clone(state.Properties), Version: state.Version,
	})
	declared, ok := br.blockProperties[upgraded.Name]
	if !ok {
		return nil, 0, false
	}
	coerced := maps.Clone(declared)
	cost := 0
	for property, value := range upgraded.Properties {
		declaredValue, ok := declared[property]
		if !ok {
			cost++
			continue
		}
		coercedValue := coerceStateValue(value, declaredValue)
		if reflect.TypeOf(coercedValue) != reflect.TypeOf(value) {
			cost++
		}
		coerced[property] = coercedValue
	}
	b, ok := br.BlockByName(upgraded.Name, coerced)
	return b, cost, ok
}

// looseNumericCandidateProperties returns deterministic keys whose value has another valid NBT numeric encoding.
func looseNumericCandidateProperties(properties map[string]any) []string {
	keys := make([]string, 0, len(properties))
	for key, value := range properties {
		switch value := value.(type) {
		case bool:
			keys = append(keys, key)
		case uint8:
			keys = append(keys, key)
		case int32:
			if value == 0 || value == 1 {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

// alternateNumericEncoding switches a byte to an integer, or a boolean-like integer to a byte.
func alternateNumericEncoding(value any) any {
	if value, ok := value.(bool); ok {
		if value {
			return uint8(1)
		}
		return uint8(0)
	}
	if value, ok := value.(uint8); ok {
		return int32(value)
	}
	return uint8(value.(int32))
}

// coerceStateValue converts one NBT property value to the type the palette
// declares for it.
//
// A block state property is only ever a byte, an int or a string, so a string
// never needs converting and only the two numeric spellings can disagree. A
// value whose declared type cannot hold it is returned unchanged, so the caller
// still fails the lookup rather than resolving to the wrong state.
func coerceStateValue(value, declared any) any {
	var number int32
	switch v := value.(type) {
	case bool:
		if v {
			number = 1
		}
	case uint8:
		number = int32(v)
	case int32:
		number = v
	default:
		return value
	}
	switch declared.(type) {
	case bool:
		switch number {
		case 0:
			return false
		case 1:
			return true
		default:
			return value
		}
	case uint8:
		if number < 0 || number > math.MaxUint8 {
			return value
		}
		return uint8(number)
	case int32:
		return number
	}
	return value
}

// CustomBlocks returns a map of all custom blocks registered with their names as keys.
func (br *BasicBlockRegistry) CustomBlocks() map[string]CustomBlock {
	return maps.Clone(br.customBlocks)
}

// Air returns an air block.
func (br *BasicBlockRegistry) Air() Block {
	if !br.finalized {
		panic("BlockRegistry.Air called on non finalized BlockRegistry")
	}
	if br.airRID >= uint32(len(br.blocks)) {
		// This should never happen for a valid registry (Finalize enforces air exists).
		panic("BlockRegistry.Air: air runtime ID out of range")
	}
	return br.blocks[br.airRID]
}

// NetworkBlockHash returns the key a client sorts its block palette by, making a block's
// runtime ID its index in that ordering. Finalize sorts by it too, so the two agree.
func NetworkBlockHash(name string) uint64 {
	return fnv1.HashString64(name)
}
