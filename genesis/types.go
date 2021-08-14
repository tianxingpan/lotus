package genesis

import (
	"encoding/json"

	"github.com/filecoin-project/go-state-types/network"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"

	market2 "github.com/filecoin-project/specs-actors/v2/actors/builtin/market"
)

type ActorType string

const (
	TAccount  ActorType = "account"
	TMultisig ActorType = "multisig"
)

// 预密封
type PreSeal struct {
	CommR     cid.Cid
	CommD     cid.Cid
	SectorID  abi.SectorNumber		// 扇区ID
	Deal      market2.DealProposal	// 交易
	ProofType abi.RegisteredSealProof	// 证明类型
}

// 矿工
type Miner struct {
	ID     address.Address
	Owner  address.Address
	Worker address.Address
	PeerId peer.ID //nolint:golint

	MarketBalance abi.TokenAmount	// 市场平衡
	PowerBalance  abi.TokenAmount	// 算力平衡

	SectorSize abi.SectorSize		// 扇区大小

	Sectors []*PreSeal				// 扇区
}

// 账户元数据
type AccountMeta struct {
	Owner address.Address // bls / secpk
}

// actor元数据
func (am *AccountMeta) ActorMeta() json.RawMessage {
	out, err := json.Marshal(am)
	if err != nil {
		panic(err)
	}
	return out
}

// 多签名元数据
type MultisigMeta struct {
	Signers         []address.Address
	Threshold       int
	VestingDuration int
	VestingStart    int
}

// actor元数据
func (mm *MultisigMeta) ActorMeta() json.RawMessage {
	out, err := json.Marshal(mm)
	if err != nil {
		panic(err)
	}
	return out
}

// 类似账户
type Actor struct {
	Type    ActorType
	Balance abi.TokenAmount

	Meta json.RawMessage
}

// 模版
type Template struct {
	NetworkVersion network.Version	// 网络版本
	Accounts       []Actor	// 账户
	Miners         []Miner	// 矿工

	NetworkName string		// 网络名称
	Timestamp   uint64 `json:",omitempty"`	// 时间戳

	VerifregRootKey  Actor	// 验证根密钥
	RemainderAccount Actor	// 账户余额
}
