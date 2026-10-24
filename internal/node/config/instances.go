package config

import (
	"encoding/json"

	"github.com/aianyai/sidera/internal/ident"
)

// instancesWire is instances.json.
type instancesWire struct {
	Format        int                     `json:"format"`
	Version       uint64                  `json:"version"`
	Instances     map[string]instanceWire `json:"instances"`
	LastOperation *operationWire          `json:"last_operation,omitempty"`
}

type instanceWire struct {
	Adapter   string                     `json:"adapter"`
	Enabled   bool                       `json:"enabled"`
	Config    map[string]json.RawMessage `json:"config,omitempty"`
	Endpoints map[string]overrideWire    `json:"endpoints,omitempty"`
}

type overrideWire struct {
	MaxQueueS    int64 `json:"max_queue_s,omitempty"`
	BacklogLimit int   `json:"backlog_limit,omitempty"`
}

func decodeInstances(data []byte) (Instances, error) {
	var w instancesWire
	if err := decodeStrict(data, &w); err != nil {
		return Instances{}, err
	}
	if err := checkHeader(w.Format, w.Version, true); err != nil {
		return Instances{}, err
	}
	out := Instances{Version: versionOf(w.Version), Items: make(map[string]Instance, len(w.Instances))}
	for _, name := range sortedKeys(w.Instances) {
		field := "instances." + name
		if err := checkName(field, name); err != nil {
			return Instances{}, err
		}
		iw := w.Instances[name]
		if err := checkName(field+".adapter", iw.Adapter); err != nil {
			return Instances{}, err
		}
		cfg, err := checkValues(iw.Config)
		if err != nil {
			return Instances{}, under(field+".config", err)
		}
		in := Instance{Adapter: iw.Adapter, Enabled: iw.Enabled, Config: cfg}
		if iw.Endpoints != nil {
			in.Endpoints = make(map[ident.EndpointID]EndpointOverride, len(iw.Endpoints))
			for _, key := range sortedKeys(iw.Endpoints) {
				efield := field + ".endpoints." + key
				id, err := parseEndpoint(efield, key)
				if err != nil {
					return Instances{}, err
				}
				ow := iw.Endpoints[key]
				o := EndpointOverride{MaxQueue: secondsToDuration(ow.MaxQueueS), BacklogLimit: ow.BacklogLimit}
				if err := under(efield, o.Validate()); err != nil {
					return Instances{}, err
				}
				in.Endpoints[id] = o
			}
		}
		out.Items[name] = in
	}
	if w.LastOperation != nil {
		op, err := w.LastOperation.operation()
		if err != nil {
			return Instances{}, err
		}
		out.LastOperation = op
	}
	return out, nil
}

// DecodeInstances decodes instances.json. The error is a *FileError that names
// the file.
func DecodeInstances(data []byte) (Instances, error) {
	i, err := decodeInstances(data)
	if err != nil {
		return Instances{}, fileErrorFor(InstancesPath, KindInstances, err)
	}
	return i, nil
}

// Encode returns the bytes of instances.json.
func (i Instances) Encode() ([]byte, error) {
	w := instancesWire{Format: FormatVersion, Version: uint64(i.Version), Instances: make(map[string]instanceWire, len(i.Items))}
	for name, in := range i.Items {
		iw := instanceWire{Adapter: in.Adapter, Enabled: in.Enabled}
		if len(in.Config) != 0 {
			iw.Config = in.Config
		}
		if len(in.Endpoints) != 0 {
			iw.Endpoints = make(map[string]overrideWire, len(in.Endpoints))
			for id, o := range in.Endpoints {
				secs, err := durationSeconds("max_queue_s", o.MaxQueue)
				if err != nil {
					return nil, err
				}
				iw.Endpoints[id.String()] = overrideWire{MaxQueueS: secs, BacklogLimit: o.BacklogLimit}
			}
		}
		w.Instances[name] = iw
	}
	op, err := operationWireOf(i.LastOperation)
	if err != nil {
		return nil, err
	}
	w.LastOperation = op
	return encodeJSON(w)
}

func cloneInstance(in Instance) Instance {
	in.Config = cloneRaw(in.Config)
	if in.Endpoints != nil {
		eps := make(map[ident.EndpointID]EndpointOverride, len(in.Endpoints))
		for id, o := range in.Endpoints {
			eps[id] = o
		}
		in.Endpoints = eps
	}
	return in
}

func cloneInstances(i Instances) Instances {
	items := make(map[string]Instance, len(i.Items))
	for name, in := range i.Items {
		items[name] = cloneInstance(in)
	}
	return Instances{Version: i.Version, Items: items, LastOperation: cloneOperation(i.LastOperation)}
}

// stable reports whether id is a runtime or direct endpoint id of the instance:
// an id that comes back when the endpoint is registered again.
func (e EndpointIDs) stable(id ident.EndpointID) bool {
	if !e.Runtime.IsZero() && e.Runtime == id {
		return true
	}
	for _, d := range e.Direct {
		if d == id {
			return true
		}
	}
	return false
}

// dropClosedSessionSettings removes, from every instance, the settings of
// endpoints that are neither stable nor open: the session endpoints that have
// closed. Their ids are random and never used again, so the settings can never
// apply. With no knowledge of the open endpoints it removes nothing.
func dropClosedSessionSettings(in Instances, ids map[string]EndpointIDs, open OpenEndpoints) Instances {
	if open == nil {
		return in
	}
	for name, inst := range in.Items {
		for id := range inst.Endpoints {
			if !ids[name].stable(id) && !open(id) {
				delete(inst.Endpoints, id)
			}
		}
		if len(inst.Endpoints) == 0 {
			inst.Endpoints = nil
		}
		in.Items[name] = inst
	}
	return in
}

// effectiveSettings are the settings of an endpoint: what an owner gave it,
// and for what they did not, the node default.
func effectiveSettings(defaults EndpointSettings, o EndpointOverride) EndpointSettings {
	s := defaults
	if o.MaxQueue != 0 {
		s.MaxQueue = o.MaxQueue
	}
	if o.BacklogLimit != 0 {
		s.BacklogLimit = o.BacklogLimit
	}
	return s
}
