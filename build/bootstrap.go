package build

import (
	"context"
	"embed"
	"path"
	"strings"

	"github.com/filecoin-project/lotus/lib/addrutil"

	"github.com/libp2p/go-libp2p-core/peer"
)

//go:embed bootstrap
var bootstrapfs embed.FS

func BuiltinBootstrap() ([]peer.AddrInfo, error) {
	// 是否禁止内部资产
	if DisableBuiltinAssets {
		return nil, nil
	}
	// 引导程序文件
	if BootstrappersFile != "" {
		spi, err := bootstrapfs.ReadFile(path.Join("bootstrap", BootstrappersFile))
		if err != nil {
			return nil, err
		}
		if len(spi) == 0 {
			return nil, nil
		}

		return addrutil.ParseAddresses(context.TODO(), strings.Split(strings.TrimSpace(string(spi)), "\n"))
	}

	return nil, nil
}
