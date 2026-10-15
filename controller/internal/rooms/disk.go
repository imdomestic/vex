package rooms

import (
	"os"
	"syscall"

	"github.com/imdomestic/chorus-testbed/controller/internal/model"
)

func sampleRootSpace(nodes []model.Node) error {
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	for i := range nodes {
		if nodes[i].Name != hostname {
			continue
		}
		var status syscall.Statfs_t
		if err = syscall.Statfs("/", &status); err != nil {
			return err
		}
		available := int64(status.Bavail) * int64(status.Bsize)
		nodes[i].RootFreeBytes = &available
	}
	return nil
}
