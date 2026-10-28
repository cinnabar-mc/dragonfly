package chunk

import (
	"bytes"
	"fmt"
	"testing"
)

func TestPalettedStorageExpansionPreservesValues(t *testing.T) {
	for _, uniform := range []uint32{0, 73} {
		for _, pos := range [][3]byte{{0, 0, 0}, {15, 15, 15}, {7, 3, 9}} {
			t.Run(fmt.Sprintf("uniform_%d/at_%v", uniform, pos), func(t *testing.T) {
				storage := emptyStorage(uniform)
				var want [16][16][16]uint32
				for x := range want {
					for y := range want[x] {
						for z := range want[x][y] {
							want[x][y][z] = uniform
						}
					}
				}
				storage.Set(pos[0], pos[1], pos[2], uniform+1)
				want[pos[0]][pos[1]][pos[2]] = uniform + 1
				checkExpandedStorage(t, storage, want)

				for i := 0; i < 300; i++ {
					x, y, z := byte(i>>8), byte(i&15), byte((i>>4)&15)
					value := uniform + uint32(i) + 2
					previousWidth := storage.bitsPerIndex
					storage.Set(x, y, z, value)
					want[x][y][z] = value
					if storage.bitsPerIndex != previousWidth {
						checkExpandedStorage(t, storage, want)
					}
				}
				checkExpandedStorage(t, storage, want)
			})
		}
	}
}

// checkExpandedStorage compares all cells and network bytes with a storage built at its final width.
func checkExpandedStorage(t *testing.T, storage *PalettedStorage, want [16][16][16]uint32) {
	t.Helper()
	reference := newPalettedStorage(make([]uint32, storage.palette.size.uint32s()), storage.palette.Clone())
	for x := range want {
		for y := range want[x] {
			for z, value := range want[x][y] {
				if got := storage.At(byte(x), byte(y), byte(z)); got != value {
					t.Fatalf("block (%d, %d, %d) = %d, want %d", x, y, z, got, value)
				}
				index := reference.palette.Index(value)
				if index == -1 {
					t.Fatalf("expected block %d missing from palette", value)
				}
				reference.setPaletteIndex(byte(x), byte(y), byte(z), uint16(index))
			}
		}
	}
	var gotBytes, wantBytes bytes.Buffer
	encodePalettedStorage(&gotBytes, storage, nil, NetworkEncoding, BlockPaletteEncoding{})
	encodePalettedStorage(&wantBytes, reference, nil, NetworkEncoding, BlockPaletteEncoding{})
	if !bytes.Equal(gotBytes.Bytes(), wantBytes.Bytes()) {
		t.Fatal("expanded storage network bytes differ from storage built at its final width")
	}
}

func TestSubChunkExpansionPreservesOtherLayers(t *testing.T) {
	sub := NewSubChunk(0)
	sub.Layer(0).Palette().Replace(func(uint32) uint32 { return 73 })
	sub.SetBlock(7, 3, 9, 1, 19)
	sub.SetBlock(15, 15, 15, 0, 74)
	for x := byte(0); x < 16; x++ {
		for y := byte(0); y < 16; y++ {
			for z := byte(0); z < 16; z++ {
				primary, secondary := uint32(73), uint32(0)
				if x == 15 && y == 15 && z == 15 {
					primary = 74
				}
				if x == 7 && y == 3 && z == 9 {
					secondary = 19
				}
				if got := sub.Block(x, y, z, 0); got != primary {
					t.Fatalf("primary block (%d, %d, %d) = %d, want %d", x, y, z, got, primary)
				}
				if got := sub.Block(x, y, z, 1); got != secondary {
					t.Fatalf("secondary block (%d, %d, %d) = %d, want %d", x, y, z, got, secondary)
				}
			}
		}
	}
}

func BenchmarkPalettedStorageExpansion(b *testing.B) {
	for _, tc := range []struct {
		name    string
		uniform uint32
		mixed   bool
	}{
		{name: "air"},
		{name: "non_air", uniform: 73},
		{name: "populated", mixed: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			fixture := emptyStorage(tc.uniform)
			if tc.mixed {
				fixture.Set(0, 0, 0, tc.uniform+1)
			}
			value := tc.uniform + 2
			var storage *PalettedStorage
			b.ReportAllocs()
			for b.Loop() {
				storage = fixture.Clone()
				storage.Set(7, 3, 9, value)
			}
			if got := storage.At(7, 3, 9); got != value {
				b.Fatalf("written block = %d, want %d", got, value)
			}
		})
	}
}
