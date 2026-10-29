package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

type traceLine struct {
	Gen   int    `json:"gen"`
	Point string `json:"point"`
	Path  string `json:"path"`
	From  string `json:"from"`
	Kind  string `json:"kind"`
	Crash bool   `json:"crash,omitempty"`
}

// ProcHit belongs to a CLI process and never contributes transaction coverage.
type ProcHit struct {
	Point string
}

// Trace contains each transaction arrival once, with termination evidence kept
// separately. Reached requires both the marker and the process's abnormal exit.
type Trace struct {
	Hits    []crashwalk.Hit
	Proc    []ProcHit
	Crash   string
	Reached bool
}

func readTrace(path string, gen, setup int, table *txn.Table, prefix bool) (Trace, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Trace{}, 0, err
	}
	return parseTrace(raw, gen, setup, table, prefix)
}

func parseTrace(raw []byte, gen, setup int, table *txn.Table, prefix bool) (Trace, int, error) {
	var out Trace
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		if !prefix {
			return out, 0, fmt.Errorf("acceptance: incomplete trace line")
		}
		raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	}
	kinds := map[string]string{}
	for _, k := range table.Keys() {
		kind := "normal"
		if k.Kind == txn.Recovery {
			kind = "recovery"
		}
		kinds[k.Key.String()] = kind
	}
	counts := map[fsx.Key]int{}
	var previous traceLine
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(raw) == 0 {
		return out, 0, nil
	}
	for i, line := range lines {
		var r traceLine
		if err := strictjson.Decode(line, &r); err != nil {
			return out, 0, fmt.Errorf("acceptance: trace line %d: %w", i+1, err)
		}
		if r.Gen != gen {
			return out, 0, fmt.Errorf("acceptance: trace generation %d, want %d", r.Gen, gen)
		}
		if out.Crash != "" {
			return out, 0, fmt.Errorf("acceptance: observation after crash marker")
		}
		if r.Kind == "proc" {
			if r.Point != "proc/cli.recv-after-response" || r.Path != "" || r.From != "" {
				return out, 0, fmt.Errorf("acceptance: invalid process point")
			}
		} else {
			p, err := fsx.ParsePoint(r.Point)
			if err != nil {
				return out, 0, err
			}
			if kind, ok := kinds[p.Key.String()]; !ok || kind != r.Kind {
				return out, 0, fmt.Errorf("acceptance: undeclared point or wrong kind: %s %s", r.Point, r.Kind)
			}
			if !r.Crash {
				counts[p.Key]++
				if p.Hit != counts[p.Key] {
					return out, 0, fmt.Errorf("acceptance: ordinal %s, want %d", r.Point, counts[p.Key])
				}
				out.Hits = append(out.Hits, crashwalk.NewHit(gen, fsx.Observation{Point: p, Path: r.Path, From: r.From}, i < setup))
			}
		}
		if r.Crash {
			x := r
			x.Crash = false
			if i == 0 || x != previous {
				return out, 0, fmt.Errorf("acceptance: crash marker has no matching arrival")
			}
			out.Crash = r.Point
		} else if r.Kind == "proc" {
			out.Proc = append(out.Proc, ProcHit{Point: r.Point})
		}
		previous = r
	}
	return out, len(lines), nil
}

func resultBody(raw json.RawMessage, out any) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	return json.Unmarshal(envelope.Result, out)
}
