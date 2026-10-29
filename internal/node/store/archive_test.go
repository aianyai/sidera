package store_test

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// The lines of the format, written out by hand. The checksum covers exactly the
// bytes of the record that follow `"rec":` and precede the closing brace.
var archiveLines = struct{ summaryDelivered, summaryQuarantined string }{
	summaryDelivered:   "{\"crc\":\"b31f15ae\",\"rec\":{\"k\":\"summary\",\"at\":\"2026-10-24T13:00:00Z\",\"kind\":\"in\",\"key\":\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd ep1zvf3xycnzvf3xycnzvf3xycnzvl5u4cd msg-0001 ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"state\":\"delivered\",\"accepted_at\":\"2026-10-24T09:00:00Z\",\"closed_at\":\"2026-10-24T09:05:00Z\",\"delivered_at\":\"2026-10-24T09:05:00Z\",\"size\":17,\"digest\":\"ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc\",\"quarantined\":false}}\n",
	summaryQuarantined: "{\"crc\":\"165f2d20\",\"rec\":{\"k\":\"summary\",\"at\":\"2026-10-24T13:00:00Z\",\"kind\":\"out\",\"key\":\"node1qgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpqyqszqgpq7haevd ep1zvf3xycnzvf3xycnzvf3xycnzvl5u4cd msg-0001 ep1zgfpyysjzgfpyysjzgfpyysjzgtln4pc\",\"state\":\"closed_unconfirmed\",\"reason\":\"report_by_passed\",\"size\":0,\"quarantined\":true}}\n",
}

const bodyHash = "ea8c004183bd3bd33e27907ed5cb35a24808cb99ab2bb41a727d0c675f5bcdfc"

func summaryDelivered() store.ArchiveSummary {
	return store.ArchiveSummary{
		At: time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC), Kind: store.KindIn, Key: tgtKey(1).String(),
		State: "delivered", AcceptedAt: base, ClosedAt: base.Add(5 * time.Minute), DeliveredAt: base.Add(5 * time.Minute),
		Size: 17, Digest: bodyHash,
	}
}

func summaryQuarantined() store.ArchiveSummary {
	return store.ArchiveSummary{
		At: time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC), Kind: store.KindOut, Key: tgtKey(1).String(),
		State: "closed_unconfirmed", Reason: "report_by_passed", Quarantined: true,
	}
}

func archiveSeeds() [][]byte {
	l := archiveLines
	return [][]byte{
		[]byte("\n" + l.summaryDelivered + l.summaryQuarantined),
		[]byte(l.summaryQuarantined + "\n\n" + l.summaryDelivered + l.summaryQuarantined[:20]),
		nil,
		[]byte("\n"),
	}
}

const dayFile = "state/archive/2026-10-24.jsonl"

func readFile(t *testing.T, r *rig, p string) string {
	t.Helper()
	b, err := r.mem.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", p, err)
	}
	return string(b)
}

// A batch is written as the lines above, after a newline, to the file of
// the UTC date of the call.
func TestArchiveLinesAndFileNames(t *testing.T) {
	r := newRig(t)
	l := archiveLines
	err := r.st.ArchiveSummaries(time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC), []store.ArchiveSummary{summaryDelivered(), summaryQuarantined()})
	if err != nil {
		t.Fatalf("ArchiveSummaries: %v", err)
	}
	if got, want := readFile(t, r, dayFile), "\n"+l.summaryDelivered+l.summaryQuarantined; got != want {
		t.Errorf("after ArchiveSummaries:\n got %q\nwant %q", got, want)
	}
	got, err := r.st.ReadArchive("2026-10-24.jsonl")
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if got.Dropped != 0 || len(got.Summaries) != 2 {
		t.Fatalf("ReadArchive = %d summaries, %d dropped, want 2, 0", len(got.Summaries), got.Dropped)
	}
	if !equalRecords(got.Summaries[0], summaryDelivered()) || !equalRecords(got.Summaries[1], summaryQuarantined()) {
		t.Errorf("summaries = %#v", got.Summaries)
	}
	// The next day is another file.
	if err := r.st.ArchiveSummaries(base.Add(15*time.Hour), []store.ArchiveSummary{summaryDelivered()}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, r, "state/archive/2026-10-25.jsonl"); got != "\n"+l.summaryDelivered {
		t.Errorf("file of the next day = %q", got)
	}
	// A time in another zone names the file of its UTC date.
	east := time.FixedZone("east", 14*3600)
	if err := r.st.ArchiveSummaries(time.Date(2026, 11, 1, 2, 0, 0, 0, east), []store.ArchiveSummary{summaryDelivered()}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mem.Stat("state/archive/2026-10-31.jsonl"); err != nil {
		t.Errorf("the file of the UTC date 2026-10-31: %v", err)
	}
}

func TestArchiveDayNamesTheUTCDate(t *testing.T) {
	east := time.FixedZone("east", 14*3600)
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 10, 24, 23, 59, 59, 0, time.UTC), "2026-10-24.jsonl"},
		{time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), "2026-10-25.jsonl"},
		{time.Date(2026, 10, 25, 1, 0, 0, 0, east), "2026-10-24.jsonl"},
		{time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "2026-01-02.jsonl"},
	} {
		if got := store.ArchiveDay(c.t); got != c.want {
			t.Errorf("ArchiveDay(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestExpiredArchiveFiles(t *testing.T) {
	const day = 24 * time.Hour
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	names := []string{
		"2026-09-23.jsonl", // 31 days before: expired
		"2026-09-24.jsonl", // exactly 30 days before: kept
		"2026-10-24.jsonl",
		"2026-10-25.jsonl", // in the future: kept
		"2025-02-30.jsonl", // not a date
		"2026-09-01.jsonl.corrupt",
		"notes.txt",
		".tmp-abc",
		"2026-9-1.jsonl",
		"2026-08-01.jsonl",
	}
	got := store.ExpiredArchiveFiles(names, now, 30*day)
	want := []string{"2026-09-23.jsonl", "2026-08-01.jsonl"}
	if len(got) != len(want) {
		t.Fatalf("ExpiredArchiveFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ExpiredArchiveFiles[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := store.ExpiredArchiveFiles(names, now, 1000*day); len(got) != 0 {
		t.Errorf("with a retention of 1000 days, expired = %v", got)
	}
	// The cutoff is the retention before the start of the date of now, so a
	// retention that is not a whole number of days falls between two files.
	if got := store.ExpiredArchiveFiles([]string{"2026-10-21.jsonl", "2026-10-22.jsonl", "2026-10-23.jsonl"}, now, 36*time.Hour); len(got) != 2 || got[1] != "2026-10-22.jsonl" {
		t.Errorf("with a retention of 36 hours, expired = %v, want the files of the 21st and the 22nd", got)
	}
}

// The reader's rules, each with the input that shows it.
func TestParseArchiveRules(t *testing.T) {
	l := archiveLines
	tamper := func(s, old, new string) string { return strings.Replace(s, old, new, 1) }
	for _, c := range []struct {
		name      string
		data      string
		summaries int
		dropped   int
	}{
		{"an empty file", "", 0, 0},
		{"empty lines are skipped and not counted", "\n\n" + l.summaryDelivered + "\n\n\n" + l.summaryDelivered, 2, 0},
		{"a last line without a newline is a torn tail and is ignored", l.summaryDelivered + l.summaryQuarantined[:len(l.summaryQuarantined)-1], 1, 0},
		{"a torn tail that would have been intact is ignored all the same", l.summaryDelivered + strings.TrimSuffix(l.summaryQuarantined, "\n"), 1, 0},
		{"a wrong checksum drops the line", strings.Replace(l.summaryDelivered, `"crc":"b31f15ae"`, `"crc":"b31f15af"`, 1) + l.summaryQuarantined, 1, 1},
		{"a flipped byte in the record drops the line and the next one is read", tamper(l.summaryDelivered, `"delivered"`, `"deliveree"`) + l.summaryQuarantined, 1, 1},
		{"a line that is not JSON drops and the next is read", "garbage\n" + l.summaryDelivered, 1, 1},
		{"a wrong prefix drops the line", tamper(l.summaryDelivered, `{"crc":`, `{"crd":`) + l.summaryQuarantined, 1, 1},
		{"a missing closing brace drops the line", strings.TrimSuffix(l.summaryDelivered, "}\n") + "\n" + l.summaryQuarantined, 1, 1},
		{"an unknown field in the record drops the line", tamper(l.summaryDelivered, `"state":"delivered"`, `"state":"delivered","z":1`) + l.summaryQuarantined, 1, 1},
		{"a record of another kind drops the line", tamper(l.summaryDelivered, `"k":"summary"`, `"k":"zz"`) + l.summaryQuarantined, 1, 1},
		{"zero bytes of a torn block are a dropped line in the middle", l.summaryDelivered + strings.Repeat("\x00", 40) + "\n" + l.summaryQuarantined, 2, 1},
		{"zero bytes of a torn tail are ignored", l.summaryDelivered + strings.Repeat("\x00", 40), 1, 0},
		{"garbage that holds a newline is two dropped pieces and no more", l.summaryDelivered + "ab\ncd\n" + l.summaryQuarantined, 2, 2},
		{"a carriage return does not hide a line", l.summaryDelivered + strings.TrimSuffix(l.summaryQuarantined, "\n") + "\r\n", 1, 1},
		{"invalid UTF-8 drops the line", l.summaryDelivered + "\xff\xfe{\"crc\"\n" + l.summaryQuarantined, 2, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := store.ParseArchive([]byte(c.data))
			if len(got.Summaries) != c.summaries || got.Dropped != c.dropped {
				t.Errorf("ParseArchive = %d summaries, %d dropped; want %d, %d",
					len(got.Summaries), got.Dropped, c.summaries, c.dropped)
			}
		})
	}
}

// A transaction that appends starts with a newline, so a torn tail left by a
// crash is a line of its own and does not swallow the first line that follows;
// without it the line after a torn tail would be lost with it.
func TestFirstAppendOfATransactionStartsAfterATornTail(t *testing.T) {
	r := newRig(t)
	if err := r.st.ArchiveSummaries(base, []store.ArchiveSummary{summaryQuarantined()}); err != nil {
		t.Fatal(err)
	}
	// A crash tears the tail of the file: half a line, no newline.
	torn := archiveLines.summaryQuarantined[:30]
	appendRaw(t, r.mem, dayFile, torn)
	if err := r.st.ArchiveSummaries(time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC), []store.ArchiveSummary{summaryDelivered()}); err != nil {
		t.Fatal(err)
	}
	got, err := r.st.ReadArchive("2026-10-24.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Summaries) != 2 || !equalRecords(got.Summaries[0], summaryQuarantined()) || !equalRecords(got.Summaries[1], summaryDelivered()) {
		t.Fatalf("the summary written after a torn tail was lost: %#v", got)
	}
	if got.Dropped != 1 {
		t.Errorf("dropped %d, want 1 (the torn line stays a dropped piece)", got.Dropped)
	}
}

// appendRaw adds bytes to a file of the in-memory disk without any structure,
// the way a crash leaves them.
func appendRaw(t *testing.T, mem *fsxtest.Mem, p, s string) {
	t.Helper()
	f, err := mem.OpenAppend(setupTag, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// An append that fails halfway leaves half a line in the middle of the file,
// and the Tx that made it is failed: nothing more is written by it. The next
// transaction opens the file afresh, starts after a newline, and its lines
// read intact.
func TestFailedAppendLeavesAHalfLineAndLaterTransactionsStayReadable(t *testing.T) {
	mem := fsxtest.NewMem()
	faulty := fsxtest.NewFaulty(t, mem)
	for _, d := range store.StateDirs() {
		if err := fsx.MakeDir(mem, setupTag, d); err != nil {
			t.Fatal(err)
		}
	}
	tab, err := txn.NewTable(append(txn.Internal(), store.Transactions()...)...)
	if err != nil {
		t.Fatal(err)
	}
	w := txn.New(faulty, tab, nil)
	if err := w.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	st := store.New(w)

	faulty.FailShortWrite(fsxtest.Match{Tx: "archive-summaries", Role: "archive", Step: fsx.StepWrite, Hit: 1}, 20, syscall.EIO)
	if err := st.ArchiveSummaries(base, []store.ArchiveSummary{summaryDelivered(), summaryQuarantined()}); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	half, _ := mem.ReadFile(dayFile)
	if len(half) == 0 || bytes.HasSuffix(half, []byte("\n")) {
		t.Fatalf("expected half a line in the file, got %q", half)
	}
	if err := st.ArchiveSummaries(base, []store.ArchiveSummary{summaryQuarantined()}); err != nil {
		t.Fatalf("a later transaction: %v", err)
	}
	if err := st.ArchiveSummaries(time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC), []store.ArchiveSummary{summaryDelivered()}); err != nil {
		t.Fatalf("a later write of summaries: %v", err)
	}
	got, err := st.ReadArchive("2026-10-24.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Summaries) != 2 || got.Dropped != 1 {
		t.Errorf("summaries %d dropped %d, want 2, 1: the half line must be one dropped piece and every later line intact",
			len(got.Summaries), got.Dropped)
	}
}

// The summaries are a transaction of their own: one whose append failed is not
// fatal, and the sweep that follows, which writes no archive line, removes the
// records all the same. A sweep that waited for its summaries could not free the
// space of a full disk.
func TestAFailedAppendOfSummariesDoesNotStopTheSweepThatFollows(t *testing.T) {
	r := newRig(t)
	rec := srcOutFinished(1)
	if err := r.st.SourceUpdate(rec); err != nil {
		t.Fatal(err)
	}
	r2 := newFaultyRig(t, r.mem)
	r2.faulty.Fail(fsxtest.Match{Tx: "archive-summaries", Role: "archive", Step: fsx.StepWrite, Hit: 1}, syscall.EIO)
	err := r2.st.ArchiveSummaries(time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC), []store.ArchiveSummary{summaryDelivered()})
	if err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if txn.IsFatal(err) {
		t.Errorf("a failed append has no commit point and is not fatal: %v", err)
	}
	if err := r2.st.ArchiveSweep(store.SweepChunk{Outs: []store.OutRecord{rec}}); err != nil {
		t.Fatalf("the sweep after the failed append: %v", err)
	}
	if _, err := r.mem.Stat(store.OutDir + "/" + rec.Name()); err == nil {
		t.Errorf("the record is still there: a sweep waited for a summary that was not written")
	}
}

// A sweep appends nothing to the archive, whatever it removes.
func TestASweepWritesNothingToTheArchive(t *testing.T) {
	r := newRig(t)
	rec := srcOutFinished(1)
	if err := r.st.SourceUpdate(rec); err != nil {
		t.Fatal(err)
	}
	if err := r.st.ArchiveSweep(store.SweepChunk{Outs: []store.OutRecord{rec}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mem.Stat(dayFile); err == nil {
		t.Errorf("the sweep wrote %s", dayFile)
	}
	if entries, err := r.mem.ReadDir(store.ArchiveDir); err == nil && len(entries) != 0 {
		t.Errorf("the sweep left %d files in the archive", len(entries))
	}
}
