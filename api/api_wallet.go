package api

import (
	"context"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/crypto"

	"github.com/filecoin-project/lotus/chain/types"
)

type MsgType string

const (
	MTUnknown = "unknown"

	// Signing message CID. MsgMeta.Extra contains raw cbor message bytes
	// 签名消息 CID。 MsgMeta.Extra 包含原始 cbor 消息字节
	MTChainMsg = "message"

	// Signing a blockheader. signing raw cbor block bytes (MsgMeta.Extra is empty)
	// 签署区块头。对原始 cbor 块字节进行签名（MsgMeta.Extra 为空）
	MTBlock = "block"

	// Signing a deal proposal. signing raw cbor proposal bytes (MsgMeta.Extra is empty)
	// 签署交易提案。签署原始 cbor 提议字节（MsgMeta.Extra 为空）
	MTDealProposal = "dealproposal"

	// TODO: Deals, Vouchers, VRF
)

type MsgMeta struct {
	Type MsgType

	// Additional data related to what is signed. Should be verifiable with the
	// signed bytes (e.g. CID(Extra).Bytes() == toSign)
	// 与签名内容相关的其他数据。应该可以使用带符号的字节进行验证（例如 CID(Extra).Bytes() == toSign）
	Extra []byte
}

// 钱包的API定义
type Wallet interface {
	WalletNew(context.Context, types.KeyType) (address.Address, error) //perm:admin
	WalletHas(context.Context, address.Address) (bool, error)          //perm:admin
	WalletList(context.Context) ([]address.Address, error)             //perm:admin

	WalletSign(ctx context.Context, signer address.Address, toSign []byte, meta MsgMeta) (*crypto.Signature, error) //perm:admin

	WalletExport(context.Context, address.Address) (*types.KeyInfo, error) //perm:admin
	WalletImport(context.Context, *types.KeyInfo) (address.Address, error) //perm:admin
	WalletDelete(context.Context, address.Address) error                   //perm:admin
}
