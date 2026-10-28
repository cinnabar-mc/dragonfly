package world

import (
	"sync"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/google/uuid"
	"io"
)

// Provider represents a value that may provide world data to a World value. It usually does the reading and
// writing of the world data so that the World may use it.
type Provider interface {
	io.Closer
	// Settings loads the settings for a World and returns them.
	Settings() *Settings
	// SaveSettings saves the settings of a World.
	SaveSettings(*Settings)

	// LoadPlayerSpawnPosition loads the player spawn point if found, otherwise an error will be returned.
	LoadPlayerSpawnPosition(uuid uuid.UUID) (pos cube.Pos, exists bool, err error)
	// SavePlayerSpawnPosition saves the player spawn point. In vanilla, this can be done with beds in the overworld
	// or respawn anchors in the nether.
	SavePlayerSpawnPosition(uuid uuid.UUID, pos cube.Pos) error
	// LoadColumn reads a world.Column from the DB at a position and dimension
	// in the DB. If no column at that position exists, errors.Is(err,
	// ErrColumnNotFound) equals true.
	LoadColumn(pos ChunkPos, dim Dimension) (*chunk.Column, error)
	// StoreColumn stores a world.Column at a position and dimension in the DB.
	// An error is returned if storing was unsuccessful.
	StoreColumn(pos ChunkPos, dim Dimension, col *chunk.Column) error
}

// ConcurrentColumnReader is an optional provider capability. Returning true
// permits LoadColumn to overlap other provider calls. Reads must be thread-safe
// and return one consistent column even while its keys are being updated.
// The capability must not change while the provider is in use by a World.
type ConcurrentColumnReader interface {
	Provider
	ConcurrentColumnReads() bool
}

// Compile time check to make sure NopProvider implements Provider.
var _ Provider = (*NopProvider)(nil)

// NopProvider implements a Provider that does not perform any disk I/O. It generates values on the run and
// dynamically, instead of reading and writing data, and otherwise returns empty values. A Settings struct can be passed
// to initialise a world with specific settings. Since Settings is a pointer, using the same NopProvider for multiple
// worlds means those worlds will share the same settings.
type NopProvider struct {
	Set *Settings
}

func (n NopProvider) Settings() *Settings {
	if n.Set == nil {
		return defaultSettings()
	}
	return n.Set
}
func (NopProvider) SaveSettings(*Settings) {}
func (NopProvider) LoadColumn(ChunkPos, Dimension) (*chunk.Column, error) {
	return nil, ErrColumnNotFound
}
func (NopProvider) StoreColumn(ChunkPos, Dimension, *chunk.Column) error { return nil }
func (NopProvider) LoadPlayerSpawnPosition(uuid.UUID) (cube.Pos, bool, error) {
	return cube.Pos{}, false, nil
}
func (NopProvider) SavePlayerSpawnPosition(uuid.UUID, cube.Pos) error { return nil }
func (NopProvider) Close() error                                      { return nil }

// lockedProvider serialises provider calls except explicitly safe column reads.
type lockedProvider struct {
	mu sync.Mutex
	p  Provider
}

func (l *lockedProvider) Settings() *Settings {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.p.Settings()
}

func (l *lockedProvider) SaveSettings(s *Settings) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.p.SaveSettings(s)
}

func (l *lockedProvider) LoadPlayerSpawnPosition(id uuid.UUID) (cube.Pos, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.p.LoadPlayerSpawnPosition(id)
}

func (l *lockedProvider) SavePlayerSpawnPosition(id uuid.UUID, pos cube.Pos) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.p.SavePlayerSpawnPosition(id, pos)
}

func (l *lockedProvider) LoadColumn(pos ChunkPos, dim Dimension) (*chunk.Column, error) {
	if p, ok := l.p.(ConcurrentColumnReader); ok && p.ConcurrentColumnReads() {
		return p.LoadColumn(pos, dim)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.p.LoadColumn(pos, dim)
}

func (l *lockedProvider) StoreColumn(pos ChunkPos, dim Dimension, col *chunk.Column) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.p.StoreColumn(pos, dim, col)
}

func (l *lockedProvider) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.p.Close()
}
