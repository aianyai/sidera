package store_test

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// liveFiles writes n live record files under a data directory on the real file
// system, two thirds of them target records and one third source records, as a
// node with a full retention window holds them. The files are written with the
// operating system directly: the point is what a start has to read, and writing
// 200,000 files durably would take longer than anything the benchmark measures.
func liveFiles(tb testing.TB, n int) string {
	tb.Helper()
	root := tb.TempDir()
	for _, d := range store.StateDirs() {
		if err := os.Mkdir(filepath.Join(root, d), 0o700); err != nil {
			tb.Fatal(err)
		}
	}
	for i := 1; i <= n; i++ {
		var rec store.Record
		var dir string
		if i%3 == 0 {
			rec, dir = srcOut(i, false), store.OutDir
		} else {
			in := tgtIn(i)
			in.Rev = 1
			rec, dir = in, store.InDir
		}
		data, err := store.EncodeRecord(rec)
		if err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, rec.Name()), data, 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

func scanOf(tb testing.TB, root string) *store.Scan {
	tb.Helper()
	fs, err := fsx.OpenOS(root, rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tab, err := programTable()
	if err != nil {
		tb.Fatal(err)
	}
	sc, err := store.New(txn.New(fs, tab, nil)).Scan()
	if err != nil {
		tb.Fatal(err)
	}
	return sc
}

// The fixture of the benchmark scans back whole.
func TestColdStartFixtureScansBack(t *testing.T) {
	sc := scanOf(t, liveFiles(t, 3000))
	if len(sc.In) != 2000 || len(sc.Out) != 1000 || len(sc.Damaged)+len(sc.Unknown) != 0 {
		t.Errorf("scan found %d in, %d out, %d damaged, %d unknown", len(sc.In), len(sc.Out), len(sc.Damaged), len(sc.Unknown))
	}
}

// BenchmarkColdStartScan is the time a start spends reading and decoding the live
// files of state/ (steps 1 to 4 of the validation of every file), for 10,000,
// 50,000 and 200,000 files, on the real file system. The page cache is warm: the
// files were just written, and dropping the cache needs privileges the benchmark
// does not have; a cold cache is slower and is not measured here.
func BenchmarkColdStartScan(b *testing.B) {
	for _, n := range []int{10_000, 50_000, 200_000} {
		b.Run(fmt.Sprintf("%dk", n/1000), func(b *testing.B) {
			root := liveFiles(b, n)
			b.ResetTimer()
			var last time.Duration
			for i := 0; i < b.N; i++ {
				start := time.Now()
				sc := scanOf(b, root)
				last = time.Since(start)
				if len(sc.In)+len(sc.Out) != n {
					b.Fatalf("scan found %d records, want %d", len(sc.In)+len(sc.Out), n)
				}
			}
			b.ReportMetric(last.Seconds(), "s/scan")
			b.ReportMetric(float64(n)/last.Seconds(), "files/s")
		})
	}
}
