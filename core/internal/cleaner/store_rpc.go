package cleaner

import (
	"fmt"
	"strings"
	"time"

	v1 "github.com/RA341/dockman/generated/cleaner/v1"
)

func (pc *PruneConfig) ToProto() *v1.PruneConfig {
	cronExpression := strings.TrimSpace(pc.CronExpression)
	if cronExpression == "" {
		if pc.Interval > 0 {
			cronExpression = fmt.Sprintf("@every %s", pc.Interval)
		} else {
			cronExpression = defaultPruneCron
		}
	}
	return &v1.PruneConfig{
		Enabled:         pc.Enabled,
		IntervalInHours: uint32(pc.Interval.Hours()),
		Volumes:         pc.Volumes,
		Networks:        pc.Networks,
		Images:          pc.Images,
		Containers:      pc.Containers,
		BuildCache:      pc.BuildCache,
		CronExpression:  cronExpression,
	}
}

func (pc *PruneConfig) FromProto(rpcConf *v1.PruneConfig) {
	pc.Enabled = rpcConf.Enabled
	pc.Interval = time.Duration(rpcConf.IntervalInHours) * time.Hour
	pc.CronExpression = strings.TrimSpace(rpcConf.CronExpression)
	pc.Volumes = rpcConf.Volumes
	pc.Networks = rpcConf.Networks
	pc.Images = rpcConf.Images
	pc.Containers = rpcConf.Containers
	pc.BuildCache = rpcConf.BuildCache
}
