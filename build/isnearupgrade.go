package build

import (
	"github.com/filecoin-project/go-state-types/abi"
)

// 是否即将升级
func IsNearUpgrade(epoch, upgradeEpoch abi.ChainEpoch) bool {
	return epoch > upgradeEpoch-Finality && epoch < upgradeEpoch+Finality
}
