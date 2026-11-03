package process

import "github.com/aianyai/sidera/sdk/go/adapterproto"

// place says how the process is started here: as asked, in a process group of its
// own, which Setsid makes. Nothing else moves it out of the adapter's reach.
func place(spec Spec) ([]string, adapterproto.Container, Detachment, error) {
	return spec.Argv, adapterproto.Container{Kind: adapterproto.ContainerProcessGroup}, hostDetachment(), nil
}

func hostDetachment() Detachment { return Detachment{OutlivesDaemon: true} }
