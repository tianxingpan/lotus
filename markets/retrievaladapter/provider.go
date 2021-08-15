package retrievaladapter

import (
	"context"
	"io"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/api/v1api"
	"github.com/filecoin-project/lotus/node/modules/dtypes"
	"github.com/filecoin-project/lotus/storage/sectorblocks"
	"github.com/hashicorp/go-multierror"
	"golang.org/x/xerrors"

	"github.com/ipfs/go-cid"

	"github.com/filecoin-project/lotus/chain/actors/builtin/paych"
	"github.com/filecoin-project/lotus/chain/types"
	sectorstorage "github.com/filecoin-project/lotus/extern/sector-storage"
	"github.com/filecoin-project/lotus/extern/sector-storage/storiface"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-fil-markets/shared"
	"github.com/filecoin-project/go-state-types/abi"
	specstorage "github.com/filecoin-project/specs-storage/storage"

	logging "github.com/ipfs/go-log/v2"
)

var log = logging.Logger("retrievaladapter")

// 检索提供者节点
type retrievalProviderNode struct {
	maddr address.Address
	secb  sectorblocks.SectorBuilder
	pp    sectorstorage.PieceProvider
	full  v1api.FullNode
}

// NewRetrievalProviderNode returns a new node adapter for a retrieval provider that talks to the
// Lotus Node
// NewRetrievalProviderNode 为与 Lotus 节点对话的检索提供者返回一个新的节点适配器
func NewRetrievalProviderNode(maddr dtypes.MinerAddress, secb sectorblocks.SectorBuilder, pp sectorstorage.PieceProvider, full v1api.FullNode) retrievalmarket.RetrievalProviderNode {
	return &retrievalProviderNode{address.Address(maddr), secb, pp, full}
}

// 获取矿工地址
func (rpn *retrievalProviderNode) GetMinerWorkerAddress(ctx context.Context, miner address.Address, tok shared.TipSetToken) (address.Address, error) {
	// TipSetKeyFromBytes包装编码密钥，验证解码是否正确。
	tsk, err := types.TipSetKeyFromBytes(tok)
	if err != nil {
		return address.Undef, err
	}

	// StateMinerInfo 返回有关指定矿工的信息
	mi, err := rpn.full.StateMinerInfo(ctx, miner, tsk)
	// 官方没有优先处理err不为空的情况，可能导致程序panic
	if err != nil {
		return address.Undef, err
	}
	return mi.Worker, err
}

// 启封扇区
func (rpn *retrievalProviderNode) UnsealSector(ctx context.Context, sectorID abi.SectorNumber, offset abi.UnpaddedPieceSize, length abi.UnpaddedPieceSize) (io.ReadCloser, error) {
	log.Debugf("get sector %d, offset %d, length %d", sectorID, offset, length)
	si, err := rpn.sectorsStatus(ctx, sectorID, false)
	if err != nil {
		return nil, err
	}

	mid, err := address.IDFromAddress(rpn.maddr)
	if err != nil {
		return nil, err
	}

	ref := specstorage.SectorRef{
		ID: abi.SectorID{
			Miner:  abi.ActorID(mid),
			Number: sectorID,
		},
		ProofType: si.SealProof,
	}

	var commD cid.Cid
	if si.CommD != nil {
		commD = *si.CommD
	}

	// Get a reader for the piece, unsealing the piece if necessary
	// 为这件作品找一位读者，必要时解开该作品
	log.Debugf("read piece in sector %d, offset %d, length %d from miner %d", sectorID, offset, length, mid)
	r, unsealed, err := rpn.pp.ReadPiece(ctx, ref, storiface.UnpaddedByteIndex(offset), length, si.Ticket.Value, commD)
	if err != nil {
		return nil, xerrors.Errorf("failed to unseal piece from sector %d: %w", sectorID, err)
	}
	_ = unsealed // todo: use

	return r, nil
}

// 保存付款凭证
func (rpn *retrievalProviderNode) SavePaymentVoucher(ctx context.Context, paymentChannel address.Address, voucher *paych.SignedVoucher, proof []byte, expectedAmount abi.TokenAmount, tok shared.TipSetToken) (abi.TokenAmount, error) {
	// TODO: respect the provided TipSetToken (a serialized TipSetKey) when
	// querying the chain
	// TODO: 在查询链时，请遵守提供的 TipSetToken（一个序列化的 TipSetKey）
	added, err := rpn.full.PaychVoucherAdd(ctx, paymentChannel, voucher, proof, expectedAmount)
	return added, err
}

// 获取链头
func (rpn *retrievalProviderNode) GetChainHead(ctx context.Context) (shared.TipSetToken, abi.ChainEpoch, error) {
	head, err := rpn.full.ChainHead(ctx)
	if err != nil {
		return nil, 0, err
	}

	return head.Key().Bytes(), head.Height(), nil
}

// 是否未密封
func (rpn *retrievalProviderNode) IsUnsealed(ctx context.Context, sectorID abi.SectorNumber, offset abi.UnpaddedPieceSize, length abi.UnpaddedPieceSize) (bool, error) {
	si, err := rpn.sectorsStatus(ctx, sectorID, true)
	if err != nil {
		return false, xerrors.Errorf("failed to get sector info: %w", err)
	}

	mid, err := address.IDFromAddress(rpn.maddr)
	if err != nil {
		return false, err
	}

	ref := specstorage.SectorRef{
		ID: abi.SectorID{
			Miner:  abi.ActorID(mid),
			Number: sectorID,
		},
		ProofType: si.SealProof,
	}

	log.Debugf("will call IsUnsealed now sector=%+v, offset=%d, size=%d", sectorID, offset, length)
	return rpn.pp.IsUnsealed(ctx, ref, storiface.UnpaddedByteIndex(offset), length)
}

// GetRetrievalPricingInput takes a set of candidate storage deals that can serve a retrieval request,
// and returns an minimally populated PricingInput. This PricingInput should be enhanced
// with more data, and passed to the pricing function to determine the final quoted price.
// GetRetrievalPricingInput 接受一组可以为检索请求提供服务的候选存储交易，并返回一个最少填充的 PricingInput。
// 应使用更多数据增强此 PricingInput，并将其传递给定价函数以确定最终报价。
func (rpn *retrievalProviderNode) GetRetrievalPricingInput(ctx context.Context, pieceCID cid.Cid, storageDeals []abi.DealID) (retrievalmarket.PricingInput, error) {
	// PricingInput 提供为检索交易定价所需的输入参数。
	resp := retrievalmarket.PricingInput{}

	head, err := rpn.full.ChainHead(ctx)
	if err != nil {
		return resp, xerrors.Errorf("failed to get chain head: %w", err)
	}
	tsk := head.Key()

	var mErr error

	for _, dealID := range storageDeals {
		// 根据dealID查询指定的交易消息
		ds, err := rpn.full.StateMarketStorageDeal(ctx, dealID, tsk)
		if err != nil {
			log.Warnf("failed to look up deal %d on chain: err=%w", dealID, err)
			mErr = multierror.Append(mErr, err)
			continue
		}
		if ds.Proposal.VerifiedDeal {
			resp.VerifiedDeal = true
		}

		if ds.Proposal.PieceCID.Equals(pieceCID) {
			resp.PieceSize = ds.Proposal.PieceSize.Unpadded()
		}

		// If we've discovered a verified deal with the required PieceCID, we don't need
		// to lookup more deals and we're done.
		// 如果我们发现了具有所需 PieceCID 的经过验证的交易，我们不需要查找更多交易，我们就完成了。
		if resp.VerifiedDeal && resp.PieceSize != 0 {
			break
		}
	}

	// Note: The piece size can never actually be zero. We only use it to here
	// to assert that we didn't find a matching piece.
	// 注意：工件尺寸实际上永远不可能为零。我们只在这里使用它来断言我们没有找到匹配的作品。
	if resp.PieceSize == 0 {
		if mErr == nil {
			// 未能找到匹配的作品
			return resp, xerrors.New("failed to find matching piece")
		}

		return resp, xerrors.Errorf("failed to fetch storage deal state: %w", mErr)
	}

	return resp, nil
}

// 扇区状态
func (rpn *retrievalProviderNode) sectorsStatus(ctx context.Context, sid abi.SectorNumber, showOnChainInfo bool) (api.SectorInfo, error) {
	sInfo, err := rpn.secb.SectorsStatus(ctx, sid, false)
	if err != nil {
		return api.SectorInfo{}, err
	}

	if !showOnChainInfo {
		return sInfo, nil
	}

	// StateSectorGetInfo 返回指定矿工扇区的链上信息。如果找不到扇区信息，则返回 null
	onChainInfo, err := rpn.full.StateSectorGetInfo(ctx, rpn.maddr, sid, types.EmptyTSK)
	if err != nil {
		return sInfo, err
	}
	if onChainInfo == nil {
		return sInfo, nil
	}
	sInfo.SealProof = onChainInfo.SealProof
	sInfo.Activation = onChainInfo.Activation
	sInfo.Expiration = onChainInfo.Expiration
	sInfo.DealWeight = onChainInfo.DealWeight
	sInfo.VerifiedDealWeight = onChainInfo.VerifiedDealWeight
	sInfo.InitialPledge = onChainInfo.InitialPledge

	// StateSectorExpiration 返回给定扇区将到期的纪元
	ex, err := rpn.full.StateSectorExpiration(ctx, rpn.maddr, sid, types.EmptyTSK)
	if err != nil {
		return sInfo, nil
	}
	sInfo.OnTime = ex.OnTime
	sInfo.Early = ex.Early

	return sInfo, nil
}
