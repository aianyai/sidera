package inproc

import (
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

func transactionBenchmarkKeysCases() []transactionBenchmarkCase {
	return []transactionBenchmarkCase{
		{name: keys.TxCertRenew, prepare: func(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
			_, w, _, key := newTransactionBenchmarkNode(b, observer)
			before := key.Certificate().Payload().Serial
			at := harness.DefaultStart.Add(time.Hour)
			return transactionBenchmarkFixture{
				invoke: func() error { return key.Renew(at) },
				verify: func() error {
					loaded, err := keys.Load(w)
					if err != nil {
						return err
					}
					if loaded.Certificate().Payload().Serial != before+1 {
						return fmt.Errorf("renewal did not persist the next serial")
					}
					clk, err := keys.ReadClock(w.Reader())
					if err == nil && !clk.HighWater.Equal(at) {
						return fmt.Errorf("renewal did not persist its signing time")
					}
					return err
				},
			}
		}},
		{name: keys.TxClockHighWater, prepare: func(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
			_, w, _, key := newTransactionBenchmarkNode(b, observer)
			at := harness.DefaultStart.Add(time.Minute)
			return transactionBenchmarkFixture{
				invoke: func() error { return key.SaveClock(keys.ClockState{HighWater: at}) },
				verify: func() error {
					got, err := keys.ReadClock(w.Reader())
					if err == nil && !got.HighWater.Equal(at) {
						return fmt.Errorf("clock high water = %s, want %s", got.HighWater, at)
					}
					return err
				},
			}
		}},
	}
}
