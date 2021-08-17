package storageadapter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"go.uber.org/fx"
	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	market2 "github.com/filecoin-project/specs-actors/v2/actors/builtin/market"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/chain/actors"
	"github.com/filecoin-project/lotus/chain/actors/builtin/market"
	"github.com/filecoin-project/lotus/chain/actors/builtin/miner"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/node/config"
	"github.com/filecoin-project/lotus/storage"
)

// 交易发布者API
type dealPublisherAPI interface {
	ChainHead(context.Context) (*types.TipSet, error)
	MpoolPushMessage(ctx context.Context, msg *types.Message, spec *api.MessageSendSpec) (*types.SignedMessage, error)
	StateMinerInfo(context.Context, address.Address, types.TipSetKey) (miner.MinerInfo, error)

	WalletBalance(context.Context, address.Address) (types.BigInt, error)
	WalletHas(context.Context, address.Address) (bool, error)
	StateAccountKey(context.Context, address.Address, types.TipSetKey) (address.Address, error)
	StateLookupID(context.Context, address.Address, types.TipSetKey) (address.Address, error)
}

// DealPublisher batches deal publishing so that many deals can be included in
// a single publish message. This saves gas for miners that publish deals
// frequently.
// When a deal is submitted, the DealPublisher waits a configurable amount of
// time for other deals to be submitted before sending the publish message.
// There is a configurable maximum number of deals that can be included in one
// message. When the limit is reached the DealPublisher immediately submits a
// publish message with all deals in the queue.
// DealPublisher 批量交易发布，以便在单个发布消息中可以包含许多交易。这为频繁发布交易的矿工节省了gas。
// 当交易被提交时，DealPublisher 在发送发布消息之前等待可配置的时间以等待其他交易的提交。
// 一条消息中可以包含可配置的最大交易数量。当达到限制时，DealPublisher 立即提交包含队列中所有交易的发布消息。
// TODO: 这里是否可以调整优化呢？
type DealPublisher struct {
	api dealPublisherAPI
	as  *storage.AddressSelector

	ctx      context.Context
	Shutdown context.CancelFunc

	maxDealsPerPublishMsg uint64
	publishPeriod         time.Duration
	publishSpec           *api.MessageSendSpec

	lk                     sync.Mutex
	pending                []*pendingDeal	// TODO：pending为待办交易信息，如果将其改造成channel类型，岂不是可以减少锁的逻辑
	cancelWaitForMoreDeals context.CancelFunc
	publishPeriodStart     time.Time
}

// A deal that is queued to be published
// 排队等待发布的交易
type pendingDeal struct {
	ctx    context.Context
	deal   market2.ClientDealProposal
	Result chan publishResult
}

// The result of publishing a deal
// 发布交易的结果
type publishResult struct {
	msgCid cid.Cid
	err    error
}

// 新的待处理交易
func newPendingDeal(ctx context.Context, deal market2.ClientDealProposal) *pendingDeal {
	return &pendingDeal{
		ctx:    ctx,
		deal:   deal,
		Result: make(chan publishResult),
	}
}

type PublishMsgConfig struct {
	// The amount of time to wait for more deals to arrive before
	// publishing
	// 在发布之前等待更多交易到达的时间
	Period time.Duration
	// The maximum number of deals to include in a single PublishStorageDeals
	// message
	// 单个 PublishStorageDeals 消息中包含的最大交易数
	MaxDealsPerMsg uint64
}

// 新订单发布者
func NewDealPublisher(
	feeConfig *config.MinerFeeConfig,
	publishMsgCfg PublishMsgConfig,
) func(lc fx.Lifecycle, full api.FullNode, as *storage.AddressSelector) *DealPublisher {
	return func(lc fx.Lifecycle, full api.FullNode, as *storage.AddressSelector) *DealPublisher {
		maxFee := abi.NewTokenAmount(0)
		if feeConfig != nil {
			maxFee = abi.TokenAmount(feeConfig.MaxPublishDealsFee)
		}
		publishSpec := &api.MessageSendSpec{MaxFee: maxFee}
		dp := newDealPublisher(full, as, publishMsgCfg, publishSpec)
		lc.Append(fx.Hook{
			OnStop: func(ctx context.Context) error {
				dp.Shutdown()
				return nil
			},
		})
		return dp
	}
}

func newDealPublisher(
	dpapi dealPublisherAPI,
	as *storage.AddressSelector,
	publishMsgCfg PublishMsgConfig,
	publishSpec *api.MessageSendSpec,
) *DealPublisher {
	ctx, cancel := context.WithCancel(context.Background())
	return &DealPublisher{
		api:                   dpapi,
		as:                    as,
		ctx:                   ctx,
		Shutdown:              cancel,
		maxDealsPerPublishMsg: publishMsgCfg.MaxDealsPerMsg,
		publishPeriod:         publishMsgCfg.Period,
		publishSpec:           publishSpec,
	}
}

// PendingDeals returns the list of deals that are queued up to be published
// PendingDeals 返回排队等待发布的交易列表
func (p *DealPublisher) PendingDeals() api.PendingDealInfo {
	p.lk.Lock()
	defer p.lk.Unlock()

	// Filter out deals whose context has been cancelled
	// 过滤掉上下文已被取消的交易
	deals := make([]*pendingDeal, 0, len(p.pending))
	for _, dl := range p.pending {
		if dl.ctx.Err() == nil {
			deals = append(deals, dl)
		}
	}

	pending := make([]market2.ClientDealProposal, len(deals))
	for i, deal := range deals {
		pending[i] = deal.deal
	}

	return api.PendingDealInfo{
		Deals:              pending,
		PublishPeriodStart: p.publishPeriodStart,
		PublishPeriod:      p.publishPeriod,
	}
}

// ForcePublishPendingDeals publishes all pending deals without waiting for
// the publish period to elapse
// ForcePublishPendingDeals 发布所有待定交易，无需等待发布期结束
func (p *DealPublisher) ForcePublishPendingDeals() {
	p.lk.Lock()
	defer p.lk.Unlock()

	log.Infof("force publishing deals")
	// 强制发布交易
	p.publishAllDeals()
}

func (p *DealPublisher) Publish(ctx context.Context, deal market2.ClientDealProposal) (cid.Cid, error) {
	pdeal := newPendingDeal(ctx, deal)

	// Add the deal to the queue
	// 将交易添加到队列中
	p.processNewDeal(pdeal)

	// Wait for the deal to be submitted
	// 等待交易提交
	select {
	case <-ctx.Done():
		return cid.Undef, ctx.Err()
	case res := <-pdeal.Result:
		return res.msgCid, res.err
	}
}

func (p *DealPublisher) processNewDeal(pdeal *pendingDeal) {
	p.lk.Lock()
	defer p.lk.Unlock()

	// Filter out any cancelled deals
	// 过滤掉所有取消的交易
	p.filterCancelledDeals()

	// If all deals have been cancelled, clear the wait-for-deals timer
	// 如果所有交易都被取消，清除等待交易计时器
	if len(p.pending) == 0 && p.cancelWaitForMoreDeals != nil {
		p.cancelWaitForMoreDeals()
		p.cancelWaitForMoreDeals = nil
	}

	// Make sure the new deal hasn't been cancelled
	// 确保新交易没有被取消
	if pdeal.ctx.Err() != nil {
		return
	}

	// Add the new deal to the queue
	// 将新交易添加到队列中
	p.pending = append(p.pending, pdeal)
	log.Infof("add deal with piece CID %s to publish deals queue - %d deals in queue (max queue size %d)",
		pdeal.deal.Proposal.PieceCID, len(p.pending), p.maxDealsPerPublishMsg)

	// If the maximum number of deals per message has been reached,
	// send a publish message
	// 如果已达到每条消息的最大交易数，则发送发布消息
	if uint64(len(p.pending)) >= p.maxDealsPerPublishMsg {
		log.Infof("publish deals queue has reached max size of %d, publishing deals", p.maxDealsPerPublishMsg)
		p.publishAllDeals()
		return
	}

	// Otherwise wait for more deals to arrive or the timeout to be reached
	// 否则等待更多交易到达或超时
	// TODO：采用同步阻塞方式，等待，如果订单多时，岂不是有大量协程处于wait状态？
	p.waitForMoreDeals()
}

// TODO: 是否将队列改成channel会更为灵活呢？
func (p *DealPublisher) waitForMoreDeals() {
	// Check if we're already waiting for deals
	// 检查我们是否已经在等待交易
	if !p.publishPeriodStart.IsZero() {
		elapsed := time.Since(p.publishPeriodStart)
		log.Infof("%s elapsed of / %s until publish deals queue is published",
			elapsed, p.publishPeriod)
		return
	}

	// Set a timeout to wait for more deals to arrive
	// 设置超时以等待更多交易到达
	log.Infof("waiting publish deals queue period of %s before publishing", p.publishPeriod)
	ctx, cancel := context.WithCancel(p.ctx)
	p.publishPeriodStart = time.Now()
	p.cancelWaitForMoreDeals = cancel

	go func() {
		timer := time.NewTimer(p.publishPeriod)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
			// 这里有加锁，外层已经加锁，容易导致死锁。
			p.lk.Lock()
			defer p.lk.Unlock()

			// The timeout has expired so publish all pending deals
			// 超时已过期，因此发布所有待处理的交易
			log.Infof("publish deals queue period of %s has expired, publishing deals", p.publishPeriod)
			p.publishAllDeals()
		}
	}()
}

func (p *DealPublisher) publishAllDeals() {
	// If the timeout hasn't yet been cancelled, cancel it
	// 如果超时尚未取消，请取消它
	if p.cancelWaitForMoreDeals != nil {
		p.cancelWaitForMoreDeals()
		p.cancelWaitForMoreDeals = nil
		p.publishPeriodStart = time.Time{}
	}

	// Filter out any deals that have been cancelled
	// 过滤掉所有已取消的交易
	p.filterCancelledDeals()
	deals := p.pending[:]
	p.pending = nil

	// Send the publish message
	// 发送发布消息
	// TODO：又启一个协程，这些消耗可能导致程序调度的消耗
	go p.publishReady(deals)
}

// 发布就绪
func (p *DealPublisher) publishReady(ready []*pendingDeal) {
	if len(ready) == 0 {
		return
	}

	// onComplete is called when the publish message has been sent or there
	// was an error
	// 当发布消息已发送或出现错误时调用 onComplete
	onComplete := func(pd *pendingDeal, msgCid cid.Cid, err error) {
		// Send the publish result on the pending deal's Result channel
		// 在挂起交易的结果通道上发送发布结果
		res := publishResult{
			msgCid: msgCid,
			err:    err,
		}
		select {
		case <-p.ctx.Done():
		case <-pd.ctx.Done():
		case pd.Result <- res:
		}
	}

	// Validate each deal to make sure it can be published
	// 验证每笔交易以确保它可以发布
	validated := make([]*pendingDeal, 0, len(ready))
	deals := make([]market2.ClientDealProposal, 0, len(ready))
	for _, pd := range ready {
		// Validate the deal
		// 验证交易
		if err := p.validateDeal(pd.deal); err != nil {
			// Validation failed, complete immediately with an error
			// 验证失败，立即完成并出现错误
			go onComplete(pd, cid.Undef, err)
			continue
		}

		validated = append(validated, pd)
		deals = append(deals, pd.deal)
	}

	// Send the publish message
	// 发送发布消息
	msgCid, err := p.publishDealProposals(deals)

	// Signal that each deal has been published
	// 表示每笔交易已发布
	for _, pd := range validated {
		go onComplete(pd, msgCid, err)
	}
}

// validateDeal checks that the deal proposal start epoch hasn't already
// elapsed
// validateDeal 检查交易提案开始时期是否还没有过去
func (p *DealPublisher) validateDeal(deal market2.ClientDealProposal) error {
	head, err := p.api.ChainHead(p.ctx)
	if err != nil {
		return err
	}
	if head.Height() > deal.Proposal.StartEpoch {
		return xerrors.Errorf(
			"cannot publish deal with piece CID %s: current epoch %d has passed deal proposal start epoch %d",
			deal.Proposal.PieceCID, head.Height(), deal.Proposal.StartEpoch)
	}
	return nil
}

// Sends the publish message
// 发送发布消息
func (p *DealPublisher) publishDealProposals(deals []market2.ClientDealProposal) (cid.Cid, error) {
	if len(deals) == 0 {
		return cid.Undef, nil
	}

	log.Infof("publishing %d deals in publish deals queue with piece CIDs: %s", len(deals), pieceCids(deals))

	provider := deals[0].Proposal.Provider
	for _, dl := range deals {
		if dl.Proposal.Provider != provider {
			msg := fmt.Sprintf("publishing %d deals failed: ", len(deals)) +
				"not all deals are for same provider: " +
				fmt.Sprintf("deal with piece CID %s is for provider %s ", deals[0].Proposal.PieceCID, deals[0].Proposal.Provider) +
				fmt.Sprintf("but deal with piece CID %s is for provider %s", dl.Proposal.PieceCID, dl.Proposal.Provider)
			return cid.Undef, xerrors.Errorf(msg)
		}
	}

	mi, err := p.api.StateMinerInfo(p.ctx, provider, types.EmptyTSK)
	if err != nil {
		return cid.Undef, err
	}

	params, err := actors.SerializeParams(&market2.PublishStorageDealsParams{
		Deals: deals,
	})

	if err != nil {
		return cid.Undef, xerrors.Errorf("serializing PublishStorageDeals params failed: %w", err)
	}

	addr, _, err := p.as.AddressFor(p.ctx, p.api, mi, api.DealPublishAddr, big.Zero(), big.Zero())
	if err != nil {
		return cid.Undef, xerrors.Errorf("selecting address for publishing deals: %w", err)
	}

	smsg, err := p.api.MpoolPushMessage(p.ctx, &types.Message{
		To:     market.Address,
		From:   addr,
		Value:  types.NewInt(0),
		Method: market.Methods.PublishStorageDeals,
		Params: params,
	}, p.publishSpec)

	if err != nil {
		return cid.Undef, err
	}
	return smsg.Cid(), nil
}

func pieceCids(deals []market2.ClientDealProposal) string {
	cids := make([]string, 0, len(deals))
	for _, dl := range deals {
		cids = append(cids, dl.Proposal.PieceCID.String())
	}
	return strings.Join(cids, ", ")
}

// filter out deals that have been cancelled
// 过滤掉已取消的交易
func (p *DealPublisher) filterCancelledDeals() {
	i := 0
	for _, pd := range p.pending {
		if pd.ctx.Err() == nil {
			p.pending[i] = pd
			i++
		}
	}
	p.pending = p.pending[:i]
}
