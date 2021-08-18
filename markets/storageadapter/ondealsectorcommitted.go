package storageadapter

import (
	"bytes"
	"context"
	"sync"

	sealing "github.com/filecoin-project/lotus/extern/storage-sealing"
	"github.com/ipfs/go-cid"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-state-types/abi"
	miner5 "github.com/filecoin-project/specs-actors/v5/actors/builtin/miner"

	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/actors/builtin/market"
	"github.com/filecoin-project/lotus/chain/actors/builtin/miner"
	"github.com/filecoin-project/lotus/chain/events"
	"github.com/filecoin-project/lotus/chain/types"
)

// 事件调用API
type eventsCalledAPI interface {
	Called(check events.CheckFunc, msgHnd events.MsgHandler, rev events.RevertHandler, confidence int, timeout abi.ChainEpoch, mf events.MsgMatchFunc) error
}

// 交易信息API
type dealInfoAPI interface {
	GetCurrentDealInfo(ctx context.Context, tok sealing.TipSetToken, proposal *market.DealProposal, publishCid cid.Cid) (sealing.CurrentDealInfo, error)
}

// 差异预提交 API
type diffPreCommitsAPI interface {
	diffPreCommits(ctx context.Context, actor address.Address, pre, cur types.TipSetKey) (*miner.PreCommitChanges, error)
}

// 扇区提交管理者
type SectorCommittedManager struct {
	ev       eventsCalledAPI
	dealInfo dealInfoAPI
	dpc      diffPreCommitsAPI
}

// 新的扇区提交管理者
func NewSectorCommittedManager(ev eventsCalledAPI, tskAPI sealing.CurrentDealInfoTskAPI, dpcAPI diffPreCommitsAPI) *SectorCommittedManager {
	dim := &sealing.CurrentDealInfoManager{
		CDAPI: &sealing.CurrentDealInfoAPIAdapter{CurrentDealInfoTskAPI: tskAPI},
	}
	return newSectorCommittedManager(ev, dim, dpcAPI)
}

func newSectorCommittedManager(ev eventsCalledAPI, dealInfo dealInfoAPI, dpcAPI diffPreCommitsAPI) *SectorCommittedManager {
	return &SectorCommittedManager{
		ev:       ev,
		dealInfo: dealInfo,
		dpc:      dpcAPI,
	}
}

// 交易扇区预提交
func (mgr *SectorCommittedManager) OnDealSectorPreCommitted(ctx context.Context, provider address.Address, proposal market.DealProposal, publishCid cid.Cid, callback storagemarket.DealSectorPreCommittedCallback) error {
	// Ensure callback is only called once
	// 确保只调用一次回调
	var once sync.Once
	cb := func(sectorNumber abi.SectorNumber, isActive bool, err error) {
		once.Do(func() {
			callback(sectorNumber, isActive, err)
		})
	}

	// First check if the deal is already active, and if so, bail out
	// 首先检查交易是否已经生效，如果已经生效，则退出
	checkFunc := func(ts *types.TipSet) (done bool, more bool, err error) {
		dealInfo, isActive, err := mgr.checkIfDealAlreadyActive(ctx, ts, &proposal, publishCid)
		if err != nil {
			// Note: the error returned from here will end up being returned
			// from OnDealSectorPreCommitted so no need to call the callback
			// with the error
			// 注意：从这里返回的错误最终会从 OnDealSectorPreCommitted 返回，因此不需要调用带有错误的回调
			return false, false, err
		}

		if isActive {
			// Deal is already active, bail out
			// 交易已经生效，退出
			cb(0, true, nil)
			return true, false, nil
		}

		// Check that precommits which landed between when the deal was published
		// and now don't already contain the deal we care about.
		// (this can happen when the precommit lands vary quickly (in tests), or
		// when the client node was down after the deal was published, and when
		// the precommit containing it landed on chain)
		// 检查在交易发布到现在之间的预提交是否已经包含我们关心的交易(当预提交区域变化很快（在测试中）
		// 时，或者当交易发布后客户端节点关闭时，以及当包含它的预提交区域在链上时，可能会发生这种情况。）

		publishTs, err := types.TipSetKeyFromBytes(dealInfo.PublishMsgTipSet)
		if err != nil {
			return false, false, err
		}

		// 差异预提交
		diff, err := mgr.dpc.diffPreCommits(ctx, provider, publishTs, ts.Key())
		if err != nil {
			return false, false, err
		}

		for _, info := range diff.Added {
			for _, d := range info.Info.DealIDs {
				if d == dealInfo.DealID {
					cb(info.Info.SectorNumber, false, nil)
					return true, false, nil
				}
			}
		}

		// Not yet active, start matching against incoming messages
		// 尚未激活，请开始与传入消息匹配
		return false, true, nil
	}

	// Watch for a pre-commit message to the provider.
	// 注意向提供程序发送预提交消息。
	matchEvent := func(msg *types.Message) (bool, error) {
		matched := msg.To == provider && (msg.Method == miner.Methods.PreCommitSector || msg.Method == miner.Methods.PreCommitSectorBatch)
		return matched, nil
	}

	// The deal must be accepted by the deal proposal start epoch, so timeout
	// if the chain reaches that epoch
	// 交易必须被交易提议开始时期接受，所以如果链到达那个时期就会超时
	timeoutEpoch := proposal.StartEpoch + 1

	// Check if the message params included the deal ID we're looking for.
	// 检查消息参数是否包含我们正在寻找的交易 ID。
	called := func(msg *types.Message, rec *types.MessageReceipt, ts *types.TipSet, curH abi.ChainEpoch) (more bool, err error) {
		defer func() {
			if err != nil {
				cb(0, false, xerrors.Errorf("handling applied event: %w", err))
			}
		}()

		// If the deal hasn't been activated by the proposed start epoch, the
		// deal will timeout (when msg == nil it means the timeout epoch was reached)
		// 如果交易没有被提议的开始时期激活，交易将超时（当 msg == nil 时表示达到了超时时期）
		if msg == nil {
			err = xerrors.Errorf("deal with piece CID %s was not activated by proposed deal start epoch %d", proposal.PieceCID, proposal.StartEpoch)
			return false, err
		}

		// Ignore the pre-commit message if it was not executed successfully
		// 如果未成功执行，则忽略预提交消息
		if rec.ExitCode != 0 {
			return true, nil
		}

		// When there is a reorg, the deal ID may change, so get the
		// current deal ID from the publish message CID
		// 当有reorg时，deal ID可能会发生变化，所以从发布消息CID中获取当前deal ID
		res, err := mgr.dealInfo.GetCurrentDealInfo(ctx, ts.Key().Bytes(), &proposal, publishCid)
		if err != nil {
			return false, err
		}

		// Extract the message parameters
		// 提取消息参数
		sn, err := dealSectorInPreCommitMsg(msg, res)
		if err != nil {
			return false, err
		}

		if sn != nil {
			cb(*sn, false, nil)
		}

		// Didn't find the deal ID in this message, so keep looking
		// 在此消息中未找到交易 ID，请继续查找
		return true, nil
	}

	revert := func(ctx context.Context, ts *types.TipSet) error {
		log.Warn("deal pre-commit reverted; TODO: actually handle this!")
		// TODO: Just go back to DealSealing?
		return nil
	}

	if err := mgr.ev.Called(checkFunc, called, revert, int(build.MessageConfidence+1), timeoutEpoch, matchEvent); err != nil {
		return xerrors.Errorf("failed to set up called handler: %w", err)
	}

	return nil
}

func (mgr *SectorCommittedManager) OnDealSectorCommitted(ctx context.Context, provider address.Address, sectorNumber abi.SectorNumber, proposal market.DealProposal, publishCid cid.Cid, callback storagemarket.DealSectorCommittedCallback) error {
	// Ensure callback is only called once
	// 确保只调用一次回调
	var once sync.Once
	cb := func(err error) {
		once.Do(func() {
			callback(err)
		})
	}

	// First check if the deal is already active, and if so, bail out
	// 首先检查交易是否已经生效，如果已经生效，则退出
	checkFunc := func(ts *types.TipSet) (done bool, more bool, err error) {
		_, isActive, err := mgr.checkIfDealAlreadyActive(ctx, ts, &proposal, publishCid)
		if err != nil {
			// Note: the error returned from here will end up being returned
			// from OnDealSectorCommitted so no need to call the callback
			// with the error
			// 注意：从这里返回的错误最终会从 OnDealSectorPreCommitted 返回，因此不需要调用带有错误的回调
			return false, false, err
		}

		if isActive {
			// Deal is already active, bail out
			// 交易已经生效，退出
			cb(nil)
			return true, false, nil
		}

		// Not yet active, start matching against incoming messages
		// 尚未激活，请开始与传入消息匹配
		return false, true, nil
	}

	// Match a prove-commit sent to the provider with the given sector number
	// 将发送给提供程序的证明提交与给定的扇区号匹配
	matchEvent := func(msg *types.Message) (matched bool, err error) {
		if msg.To != provider {
			return false, nil
		}

		return sectorInCommitMsg(msg, sectorNumber)
	}

	// The deal must be accepted by the deal proposal start epoch, so timeout
	// if the chain reaches that epoch
	// 交易必须被交易提议开始时期接受，所以如果链到达那个时期就会超时
	timeoutEpoch := proposal.StartEpoch + 1

	called := func(msg *types.Message, rec *types.MessageReceipt, ts *types.TipSet, curH abi.ChainEpoch) (more bool, err error) {
		defer func() {
			if err != nil {
				cb(xerrors.Errorf("handling applied event: %w", err))
			}
		}()

		// If the deal hasn't been activated by the proposed start epoch, the
		// deal will timeout (when msg == nil it means the timeout epoch was reached)
		// 如果交易没有被提议的开始时期激活，交易将超时（当 msg == nil 时表示达到了超时时期）
		if msg == nil {
			err := xerrors.Errorf("deal with piece CID %s was not activated by proposed deal start epoch %d", proposal.PieceCID, proposal.StartEpoch)
			return false, err
		}

		// Ignore the prove-commit message if it was not executed successfully
		// 如果没有成功执行，则忽略证明提交消息
		if rec.ExitCode != 0 {
			return true, nil
		}

		// Get the deal info
		// 获取交易信息
		res, err := mgr.dealInfo.GetCurrentDealInfo(ctx, ts.Key().Bytes(), &proposal, publishCid)
		if err != nil {
			return false, xerrors.Errorf("failed to look up deal on chain: %w", err)
		}

		// Make sure the deal is active
		// 确保交易有效
		if res.MarketDeal.State.SectorStartEpoch < 1 {
			return false, xerrors.Errorf("deal wasn't active: deal=%d, parentState=%s, h=%d", res.DealID, ts.ParentState(), ts.Height())
		}

		log.Infof("Storage deal %d activated at epoch %d", res.DealID, res.MarketDeal.State.SectorStartEpoch)

		cb(nil)

		return false, nil
	}

	revert := func(ctx context.Context, ts *types.TipSet) error {
		log.Warn("deal activation reverted; TODO: actually handle this!")
		// TODO: Just go back to DealSealing?
		return nil
	}

	if err := mgr.ev.Called(checkFunc, called, revert, int(build.MessageConfidence+1), timeoutEpoch, matchEvent); err != nil {
		return xerrors.Errorf("failed to set up called handler: %w", err)
	}

	return nil
}

// dealSectorInPreCommitMsg tries to find a sector containing the specified deal
// dealSectorInPreCommitMsg 尝试查找包含指定交易的扇区
func dealSectorInPreCommitMsg(msg *types.Message, res sealing.CurrentDealInfo) (*abi.SectorNumber, error) {
	switch msg.Method {
	case miner.Methods.PreCommitSector:
		var params miner.SectorPreCommitInfo
		if err := params.UnmarshalCBOR(bytes.NewReader(msg.Params)); err != nil {
			return nil, xerrors.Errorf("unmarshal pre commit: %w", err)
		}

		// Check through the deal IDs associated with this message
		// 检查与此消息关联的交易ID
		for _, did := range params.DealIDs {
			if did == res.DealID {
				// Found the deal ID in this message. Callback with the sector ID.
				// 在此消息中找到交易 ID。带有扇区 ID 的回调。
				return &params.SectorNumber, nil
			}
		}
	case miner.Methods.PreCommitSectorBatch:
		var params miner5.PreCommitSectorBatchParams
		if err := params.UnmarshalCBOR(bytes.NewReader(msg.Params)); err != nil {
			return nil, xerrors.Errorf("unmarshal pre commit: %w", err)
		}

		for _, precommit := range params.Sectors {
			// Check through the deal IDs associated with this message
			// 检查与此消息关联的交易 ID
			for _, did := range precommit.DealIDs {
				if did == res.DealID {
					// Found the deal ID in this message. Callback with the sector ID.
					// 在此消息中找到交易 ID。带有扇区 ID 的回调。
					return &precommit.SectorNumber, nil
				}
			}
		}
	default:
		return nil, xerrors.Errorf("unexpected method %d", msg.Method)
	}

	return nil, nil
}

// sectorInCommitMsg checks if the provided message commits specified sector
// segmentInCommitMsg 检查提供的消息是否提交了指定的扇区
func sectorInCommitMsg(msg *types.Message, sectorNumber abi.SectorNumber) (bool, error) {
	switch msg.Method {
	case miner.Methods.ProveCommitSector:
		var params miner.ProveCommitSectorParams
		if err := params.UnmarshalCBOR(bytes.NewReader(msg.Params)); err != nil {
			return false, xerrors.Errorf("failed to unmarshal prove commit sector params: %w", err)
		}

		return params.SectorNumber == sectorNumber, nil

	case miner.Methods.ProveCommitAggregate:
		var params miner5.ProveCommitAggregateParams
		if err := params.UnmarshalCBOR(bytes.NewReader(msg.Params)); err != nil {
			return false, xerrors.Errorf("failed to unmarshal prove commit sector params: %w", err)
		}

		set, err := params.SectorNumbers.IsSet(uint64(sectorNumber))
		if err != nil {
			return false, xerrors.Errorf("checking if sectorNumber is set in commit aggregate message: %w", err)
		}

		return set, nil

	default:
		return false, nil
	}
}

// 检查交易是否已经有效
func (mgr *SectorCommittedManager) checkIfDealAlreadyActive(ctx context.Context, ts *types.TipSet, proposal *market.DealProposal, publishCid cid.Cid) (sealing.CurrentDealInfo, bool, error) {
	res, err := mgr.dealInfo.GetCurrentDealInfo(ctx, ts.Key().Bytes(), proposal, publishCid)
	if err != nil {
		// TODO: This may be fine for some errors
		return res, false, xerrors.Errorf("failed to look up deal on chain: %w", err)
	}

	// Sector was slashed
	if res.MarketDeal.State.SlashEpoch > 0 {
		return res, false, xerrors.Errorf("deal %d was slashed at epoch %d", res.DealID, res.MarketDeal.State.SlashEpoch)
	}

	// Sector with deal is already active
	if res.MarketDeal.State.SectorStartEpoch > 0 {
		return res, true, nil
	}

	return res, false, nil
}
