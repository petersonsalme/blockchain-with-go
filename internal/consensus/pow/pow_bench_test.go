package pow_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

func BenchmarkMineParallel(b *testing.B) {
	for _, d := range []int{8, 12, 16} {
		for _, w := range []int{1, 2, 4, 8} {
			b.Run(fmt.Sprintf("difficulty=%d/workers=%d", d, w), func(b *testing.B) {
				prefix := strings.Repeat("0", d/4)

				for i := 0; b.Loop(); i++ {
					blk := &domain.Block{
						Index:     i + 1,
						Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 0, time.UTC),
						PrevHash:  "bench",
					}
					if err := powpkg.MineParallel(context.Background(), blk, d, w); err != nil {
						b.Fatalf("difficulty=%d workers=%d: %v", d, w, err)
					}

					if blk.Hash != blk.CalculateHash() || !strings.HasPrefix(blk.Hash, prefix) {
						b.Fatalf("difficulty=%d workers=%d: invalid hash %s", d, w, blk.Hash)
					}
				}
			})
		}
	}
}
