package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-bitfield"
	datatransfer "github.com/filecoin-project/go-data-transfer"
	"github.com/filecoin-project/go-fil-markets/retrievalmarket"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-multistore"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/filecoin-project/go-state-types/dline"

	apitypes "github.com/filecoin-project/lotus/api/types"
	"github.com/filecoin-project/lotus/chain/actors/builtin"
	"github.com/filecoin-project/lotus/chain/actors/builtin/market"
	"github.com/filecoin-project/lotus/chain/actors/builtin/miner"
	"github.com/filecoin-project/lotus/chain/actors/builtin/paych"
	"github.com/filecoin-project/lotus/chain/actors/builtin/power"
	"github.com/filecoin-project/lotus/chain/types"
	marketevents "github.com/filecoin-project/lotus/markets/loggers"
	"github.com/filecoin-project/lotus/node/modules/dtypes"
)

//go:generate go run github.com/golang/mock/mockgen -destination=mocks/mock_full.go -package=mocks . FullNode

// ChainIO abstracts operations for accessing raw IPLD objects.
type ChainIO interface {
	ChainReadObj(context.Context, cid.Cid) ([]byte, error)
	ChainHasObj(context.Context, cid.Cid) (bool, error)
}

const LookbackNoLimit = abi.ChainEpoch(-1)

//                       MODIFYING THE API INTERFACE
//
// NOTE: This is the V1 (Unstable) API - to add methods to the V0 (Stable) API
// you'll have to add those methods to interfaces in `api/v0api`
//
// When adding / changing methods in this file:
// * Do the change here
// * Adjust implementation in `node/impl/`
// * Run `make gen` - this will:
//  * Generate proxy structs
//  * Generate mocks
//  * Generate markdown docs
//  * Generate openrpc blobs

// FullNode API is a low-level interface to the Filecoin network full node
// FullNode API 是 Filecoin 网络全节点的低级接口
type FullNode interface {
	Common
	Net

	// MethodGroup: Chain
	// The Chain method group contains methods for interacting with the
	// blockchain, but that do not require any form of state computation.

	// ChainNotify returns channel with chain head updates.
	// First message is guaranteed to be of len == 1, and type == 'current'.
	// ChainNotify 返回带有链头更新的通道。
	// 第一条消息保证为 len == 1，type == 'current'。
	ChainNotify(context.Context) (<-chan []*HeadChange, error) //perm:read

	// ChainHead returns the current head of the chain.
	// ChainHead 返回链的当前头。
	ChainHead(context.Context) (*types.TipSet, error) //perm:read

	// ChainGetRandomnessFromTickets is used to sample the chain for randomness.
	// ChainGetRandomnessFromTickets 用于对链进行采样以获得随机性。
	ChainGetRandomnessFromTickets(ctx context.Context, tsk types.TipSetKey, personalization crypto.DomainSeparationTag, randEpoch abi.ChainEpoch, entropy []byte) (abi.Randomness, error) //perm:read

	// ChainGetRandomnessFromBeacon is used to sample the beacon for randomness.
	// ChainGetRandomnessFromBeacon 用于对信标进行随机采样。
	ChainGetRandomnessFromBeacon(ctx context.Context, tsk types.TipSetKey, personalization crypto.DomainSeparationTag, randEpoch abi.ChainEpoch, entropy []byte) (abi.Randomness, error) //perm:read

	// ChainGetBlock returns the block specified by the given CID.
	// ChainGetBlock 返回由给定 CID 指定的块。
	ChainGetBlock(context.Context, cid.Cid) (*types.BlockHeader, error) //perm:read
	// ChainGetTipSet returns the tipset specified by the given TipSetKey.
	// ChainGetTipSet 返回由给定 TipSetKey 指定的提示集。
	ChainGetTipSet(context.Context, types.TipSetKey) (*types.TipSet, error) //perm:read

	// ChainGetBlockMessages returns messages stored in the specified block.
	//
	// Note: If there are multiple blocks in a tipset, it's likely that some
	// messages will be duplicated. It's also possible for blocks in a tipset to have
	// different messages from the same sender at the same nonce. When that happens,
	// only the first message (in a block with lowest ticket) will be considered
	// for execution
	//
	// NOTE: THIS METHOD SHOULD ONLY BE USED FOR GETTING MESSAGES IN A SPECIFIC BLOCK
	//
	// DO NOT USE THIS METHOD TO GET MESSAGES INCLUDED IN A TIPSET
	// Use ChainGetParentMessages, which will perform correct message deduplication
	// ChainGetBlockMessages 返回存储在指定块中的消息。
	ChainGetBlockMessages(ctx context.Context, blockCid cid.Cid) (*BlockMessages, error) //perm:read

	// ChainGetParentReceipts returns receipts for messages in parent tipset of
	// the specified block. The receipts in the list returned is one-to-one with the
	// messages returned by a call to ChainGetParentMessages with the same blockCid.
	// ChainGetParentReceipts 返回指定块的父提示集中消息的回执。返回的列表中的回执与调用 ChainGetParentMessages
	// 返回的消息一一对应，具有相同的 blockCid。
	ChainGetParentReceipts(ctx context.Context, blockCid cid.Cid) ([]*types.MessageReceipt, error) //perm:read

	// ChainGetParentMessages returns messages stored in parent tipset of the
	// specified block.
	// ChainGetParentMessages 返回存储在指定块的父提示集中的消息。
	ChainGetParentMessages(ctx context.Context, blockCid cid.Cid) ([]Message, error) //perm:read

	// ChainGetMessagesInTipset returns message stores in current tipset
	// ChainGetMessagesInTipset 返回当前提示集中的消息存储
	ChainGetMessagesInTipset(ctx context.Context, tsk types.TipSetKey) ([]Message, error) //perm:read

	// ChainGetTipSetByHeight looks back for a tipset at the specified epoch.
	// If there are no blocks at the specified epoch, a tipset at an earlier epoch
	// will be returned.
	// ChainGetTipSetByHeight 在指定的 epoch 回溯寻找一个tipset。
	// 如果在指定时期没有区块，则将返回较早时期的提示集。
	ChainGetTipSetByHeight(context.Context, abi.ChainEpoch, types.TipSetKey) (*types.TipSet, error) //perm:read

	// ChainReadObj reads ipld nodes referenced by the specified CID from chain
	// blockstore and returns raw bytes.
	// ChainReadObj 从链块存储中读取指定 CID 引用的 ipld 节点并返回原始字节。
	ChainReadObj(context.Context, cid.Cid) ([]byte, error) //perm:read

	// ChainDeleteObj deletes node referenced by the given CID
	// ChainDeleteObj 删除给定 CID 引用的节点
	ChainDeleteObj(context.Context, cid.Cid) error //perm:admin

	// ChainHasObj checks if a given CID exists in the chain blockstore.
	// ChainHasObj 检查链块存储中是否存在给定的 CID。
	ChainHasObj(context.Context, cid.Cid) (bool, error) //perm:read

	// ChainStatObj returns statistics about the graph referenced by 'obj'.
	// If 'base' is also specified, then the returned stat will be a diff
	// between the two objects.
	// ChainStatObj 返回有关 'obj' 引用的图的统计信息。
	// 如果还指定了 'base'，则返回的 stat 将是两个对象之间的差异。
	ChainStatObj(ctx context.Context, obj cid.Cid, base cid.Cid) (ObjStat, error) //perm:read

	// ChainSetHead forcefully sets current chain head. Use with caution.
	// ChainSetHead 强制设置当前链头。谨慎使用。
	ChainSetHead(context.Context, types.TipSetKey) error //perm:admin

	// ChainGetGenesis returns the genesis tipset.
	// ChainGetGenesis 返回创世提示集。
	ChainGetGenesis(context.Context) (*types.TipSet, error) //perm:read

	// ChainTipSetWeight computes weight for the specified tipset.
	// ChainTipSetWeight 计算指定提示集的权重。
	ChainTipSetWeight(context.Context, types.TipSetKey) (types.BigInt, error) //perm:read
	ChainGetNode(ctx context.Context, p string) (*IpldObject, error)          //perm:read

	// ChainGetMessage reads a message referenced by the specified CID from the
	// chain blockstore.
	// ChainGetMessage 从链块存储中读取指定 CID 引用的消息。
	ChainGetMessage(context.Context, cid.Cid) (*types.Message, error) //perm:read

	// ChainGetPath returns a set of revert/apply operations needed to get from
	// one tipset to another, for example:
	//```
	//        to
	//         ^
	// from   tAA
	//   ^     ^
	// tBA    tAB
	//  ^---*--^
	//      ^
	//     tRR
	//```
	// Would return `[revert(tBA), apply(tAB), apply(tAA)]`
	// ChainGetPath 返回一组从一个提示集到另一个提示集所需的还原/应用操作，例如：
	ChainGetPath(ctx context.Context, from types.TipSetKey, to types.TipSetKey) ([]*HeadChange, error) //perm:read

	// ChainExport returns a stream of bytes with CAR dump of chain data.
	// The exported chain data includes the header chain from the given tipset
	// back to genesis, the entire genesis state, and the most recent 'nroots'
	// state trees.
	// If oldmsgskip is set, messages from before the requested roots are also not included.
	// ChainExport 返回一个字节流，其中包含链数据的 CAR 转储。
	ChainExport(ctx context.Context, nroots abi.ChainEpoch, oldmsgskip bool, tsk types.TipSetKey) (<-chan []byte, error) //perm:read

	// ChainCheckBlockstore performs an (asynchronous) health check on the chain/state blockstore
	// if supported by the underlying implementation.
	ChainCheckBlockstore(context.Context) error //perm:admin

	// ChainBlockstoreInfo returns some basic information about the blockstore
	ChainBlockstoreInfo(context.Context) (map[string]interface{}, error) //perm:read

	// MethodGroup: Beacon
	// The Beacon method group contains methods for interacting with the random beacon (DRAND)

	// BeaconGetEntry returns the beacon entry for the given filecoin epoch. If
	// the entry has not yet been produced, the call will block until the entry
	// becomes available
	BeaconGetEntry(ctx context.Context, epoch abi.ChainEpoch) (*types.BeaconEntry, error) //perm:read

	// GasEstimateFeeCap estimates gas fee cap
	GasEstimateFeeCap(context.Context, *types.Message, int64, types.TipSetKey) (types.BigInt, error) //perm:read

	// GasEstimateGasLimit estimates gas used by the message and returns it.
	// It fails if message fails to execute.
	GasEstimateGasLimit(context.Context, *types.Message, types.TipSetKey) (int64, error) //perm:read

	// GasEstimateGasPremium estimates what gas price should be used for a
	// message to have high likelihood of inclusion in `nblocksincl` epochs.

	GasEstimateGasPremium(_ context.Context, nblocksincl uint64,
		sender address.Address, gaslimit int64, tsk types.TipSetKey) (types.BigInt, error) //perm:read

	// GasEstimateMessageGas estimates gas values for unset message gas fields
	GasEstimateMessageGas(context.Context, *types.Message, *MessageSendSpec, types.TipSetKey) (*types.Message, error) //perm:read

	// MethodGroup: Sync
	// The Sync method group contains methods for interacting with and
	// observing the lotus sync service.

	// SyncState returns the current status of the lotus sync system.
	SyncState(context.Context) (*SyncState, error) //perm:read

	// SyncSubmitBlock can be used to submit a newly created block to the.
	// network through this node
	SyncSubmitBlock(ctx context.Context, blk *types.BlockMsg) error //perm:write

	// SyncIncomingBlocks returns a channel streaming incoming, potentially not
	// yet synced block headers.
	SyncIncomingBlocks(ctx context.Context) (<-chan *types.BlockHeader, error) //perm:read

	// SyncCheckpoint marks a blocks as checkpointed, meaning that it won't ever fork away from it.
	SyncCheckpoint(ctx context.Context, tsk types.TipSetKey) error //perm:admin

	// SyncMarkBad marks a blocks as bad, meaning that it won't ever by synced.
	// Use with extreme caution.
	SyncMarkBad(ctx context.Context, bcid cid.Cid) error //perm:admin

	// SyncUnmarkBad unmarks a blocks as bad, making it possible to be validated and synced again.
	SyncUnmarkBad(ctx context.Context, bcid cid.Cid) error //perm:admin

	// SyncUnmarkAllBad purges bad block cache, making it possible to sync to chains previously marked as bad
	SyncUnmarkAllBad(ctx context.Context) error //perm:admin

	// SyncCheckBad checks if a block was marked as bad, and if it was, returns
	// the reason.
	SyncCheckBad(ctx context.Context, bcid cid.Cid) (string, error) //perm:read

	// SyncValidateTipset indicates whether the provided tipset is valid or not
	SyncValidateTipset(ctx context.Context, tsk types.TipSetKey) (bool, error) //perm:read

	// MethodGroup: Mpool
	// The Mpool methods are for interacting with the message pool. The message pool
	// manages all incoming and outgoing 'messages' going over the network.

	// MpoolPending returns pending mempool messages.
	MpoolPending(context.Context, types.TipSetKey) ([]*types.SignedMessage, error) //perm:read

	// MpoolSelect returns a list of pending messages for inclusion in the next block
	MpoolSelect(context.Context, types.TipSetKey, float64) ([]*types.SignedMessage, error) //perm:read

	// MpoolPush pushes a signed message to mempool.
	MpoolPush(context.Context, *types.SignedMessage) (cid.Cid, error) //perm:write

	// MpoolPushUntrusted pushes a signed message to mempool from untrusted sources.
	MpoolPushUntrusted(context.Context, *types.SignedMessage) (cid.Cid, error) //perm:write

	// MpoolPushMessage atomically assigns a nonce, signs, and pushes a message
	// to mempool.
	// maxFee is only used when GasFeeCap/GasPremium fields aren't specified
	//
	// When maxFee is set to 0, MpoolPushMessage will guess appropriate fee
	// based on current chain conditions
	MpoolPushMessage(ctx context.Context, msg *types.Message, spec *MessageSendSpec) (*types.SignedMessage, error) //perm:sign

	// MpoolBatchPush batch pushes a signed message to mempool.
	MpoolBatchPush(context.Context, []*types.SignedMessage) ([]cid.Cid, error) //perm:write

	// MpoolBatchPushUntrusted batch pushes a signed message to mempool from untrusted sources.
	MpoolBatchPushUntrusted(context.Context, []*types.SignedMessage) ([]cid.Cid, error) //perm:write

	// MpoolBatchPushMessage batch pushes a unsigned message to mempool.
	MpoolBatchPushMessage(context.Context, []*types.Message, *MessageSendSpec) ([]*types.SignedMessage, error) //perm:sign

	// MpoolCheckMessages performs logical checks on a batch of messages
	MpoolCheckMessages(context.Context, []*MessagePrototype) ([][]MessageCheckStatus, error) //perm:read
	// MpoolCheckPendingMessages performs logical checks for all pending messages from a given address
	MpoolCheckPendingMessages(context.Context, address.Address) ([][]MessageCheckStatus, error) //perm:read
	// MpoolCheckReplaceMessages performs logical checks on pending messages with replacement
	MpoolCheckReplaceMessages(context.Context, []*types.Message) ([][]MessageCheckStatus, error) //perm:read

	// MpoolGetNonce gets next nonce for the specified sender.
	// Note that this method may not be atomic. Use MpoolPushMessage instead.
	MpoolGetNonce(context.Context, address.Address) (uint64, error) //perm:read
	MpoolSub(context.Context) (<-chan MpoolUpdate, error)           //perm:read

	// MpoolClear clears pending messages from the mpool
	MpoolClear(context.Context, bool) error //perm:write

	// MpoolGetConfig returns (a copy of) the current mpool config
	MpoolGetConfig(context.Context) (*types.MpoolConfig, error) //perm:read
	// MpoolSetConfig sets the mpool config to (a copy of) the supplied config
	MpoolSetConfig(context.Context, *types.MpoolConfig) error //perm:admin

	// MethodGroup: Miner

	MinerGetBaseInfo(context.Context, address.Address, abi.ChainEpoch, types.TipSetKey) (*MiningBaseInfo, error) //perm:read
	MinerCreateBlock(context.Context, *BlockTemplate) (*types.BlockMsg, error)                                   //perm:write

	// // UX ?

	// MethodGroup: Wallet

	// WalletNew creates a new address in the wallet with the given sigType.
	// Available key types: bls, secp256k1, secp256k1-ledger
	// Support for numerical types: 1 - secp256k1, 2 - BLS is deprecated
	WalletNew(context.Context, types.KeyType) (address.Address, error) //perm:write
	// WalletHas indicates whether the given address is in the wallet.
	WalletHas(context.Context, address.Address) (bool, error) //perm:write
	// WalletList lists all the addresses in the wallet.
	WalletList(context.Context) ([]address.Address, error) //perm:write
	// WalletBalance returns the balance of the given address at the current head of the chain.
	WalletBalance(context.Context, address.Address) (types.BigInt, error) //perm:read
	// WalletSign signs the given bytes using the given address.
	WalletSign(context.Context, address.Address, []byte) (*crypto.Signature, error) //perm:sign
	// WalletSignMessage signs the given message using the given address.
	WalletSignMessage(context.Context, address.Address, *types.Message) (*types.SignedMessage, error) //perm:sign
	// WalletVerify takes an address, a signature, and some bytes, and indicates whether the signature is valid.
	// The address does not have to be in the wallet.
	WalletVerify(context.Context, address.Address, []byte, *crypto.Signature) (bool, error) //perm:read
	// WalletDefaultAddress returns the address marked as default in the wallet.
	WalletDefaultAddress(context.Context) (address.Address, error) //perm:write
	// WalletSetDefault marks the given address as as the default one.
	WalletSetDefault(context.Context, address.Address) error //perm:write
	// WalletExport returns the private key of an address in the wallet.
	WalletExport(context.Context, address.Address) (*types.KeyInfo, error) //perm:admin
	// WalletImport receives a KeyInfo, which includes a private key, and imports it into the wallet.
	WalletImport(context.Context, *types.KeyInfo) (address.Address, error) //perm:admin
	// WalletDelete deletes an address from the wallet.
	WalletDelete(context.Context, address.Address) error //perm:admin
	// WalletValidateAddress validates whether a given string can be decoded as a well-formed address
	WalletValidateAddress(context.Context, string) (address.Address, error) //perm:read

	// Other

	// MethodGroup: Client
	// The Client methods all have to do with interacting with the storage and
	// retrieval markets as a client

	// ClientImport imports file under the specified path into filestore.
	ClientImport(ctx context.Context, ref FileRef) (*ImportRes, error) //perm:admin
	// ClientRemoveImport removes file import
	ClientRemoveImport(ctx context.Context, importID multistore.StoreID) error //perm:admin
	// ClientStartDeal proposes a deal with a miner.
	ClientStartDeal(ctx context.Context, params *StartDealParams) (*cid.Cid, error) //perm:admin
	// ClientStatelessDeal fire-and-forget-proposes an offline deal to a miner without subsequent tracking.
	ClientStatelessDeal(ctx context.Context, params *StartDealParams) (*cid.Cid, error) //perm:write
	// ClientGetDealInfo returns the latest information about a given deal.
	ClientGetDealInfo(context.Context, cid.Cid) (*DealInfo, error) //perm:read
	// ClientListDeals returns information about the deals made by the local client.
	ClientListDeals(ctx context.Context) ([]DealInfo, error) //perm:write
	// ClientGetDealUpdates returns the status of updated deals
	ClientGetDealUpdates(ctx context.Context) (<-chan DealInfo, error) //perm:write
	// ClientGetDealStatus returns status given a code
	ClientGetDealStatus(ctx context.Context, statusCode uint64) (string, error) //perm:read
	// ClientHasLocal indicates whether a certain CID is locally stored.
	ClientHasLocal(ctx context.Context, root cid.Cid) (bool, error) //perm:write
	// ClientFindData identifies peers that have a certain file, and returns QueryOffers (one per peer).
	ClientFindData(ctx context.Context, root cid.Cid, piece *cid.Cid) ([]QueryOffer, error) //perm:read
	// ClientMinerQueryOffer returns a QueryOffer for the specific miner and file.
	ClientMinerQueryOffer(ctx context.Context, miner address.Address, root cid.Cid, piece *cid.Cid) (QueryOffer, error) //perm:read
	// ClientRetrieve initiates the retrieval of a file, as specified in the order.
	ClientRetrieve(ctx context.Context, order RetrievalOrder, ref *FileRef) error //perm:admin
	// ClientRetrieveWithEvents initiates the retrieval of a file, as specified in the order, and provides a channel
	// of status updates.
	ClientRetrieveWithEvents(ctx context.Context, order RetrievalOrder, ref *FileRef) (<-chan marketevents.RetrievalEvent, error) //perm:admin
	// ClientListRetrievals returns information about retrievals made by the local client
	ClientListRetrievals(ctx context.Context) ([]RetrievalInfo, error) //perm:write
	// ClientGetRetrievalUpdates returns status of updated retrieval deals
	ClientGetRetrievalUpdates(ctx context.Context) (<-chan RetrievalInfo, error) //perm:write
	// ClientQueryAsk returns a signed StorageAsk from the specified miner.
	ClientQueryAsk(ctx context.Context, p peer.ID, miner address.Address) (*storagemarket.StorageAsk, error) //perm:read
	// ClientCalcCommP calculates the CommP and data size of the specified CID
	ClientDealPieceCID(ctx context.Context, root cid.Cid) (DataCIDSize, error) //perm:read
	// ClientCalcCommP calculates the CommP for a specified file
	ClientCalcCommP(ctx context.Context, inpath string) (*CommPRet, error) //perm:write
	// ClientGenCar generates a CAR file for the specified file.
	ClientGenCar(ctx context.Context, ref FileRef, outpath string) error //perm:write
	// ClientDealSize calculates real deal data size
	ClientDealSize(ctx context.Context, root cid.Cid) (DataSize, error) //perm:read
	// ClientListTransfers returns the status of all ongoing transfers of data
	ClientListDataTransfers(ctx context.Context) ([]DataTransferChannel, error)        //perm:write
	ClientDataTransferUpdates(ctx context.Context) (<-chan DataTransferChannel, error) //perm:write
	// ClientRestartDataTransfer attempts to restart a data transfer with the given transfer ID and other peer
	ClientRestartDataTransfer(ctx context.Context, transferID datatransfer.TransferID, otherPeer peer.ID, isInitiator bool) error //perm:write
	// ClientCancelDataTransfer cancels a data transfer with the given transfer ID and other peer
	ClientCancelDataTransfer(ctx context.Context, transferID datatransfer.TransferID, otherPeer peer.ID, isInitiator bool) error //perm:write
	// ClientRetrieveTryRestartInsufficientFunds attempts to restart stalled retrievals on a given payment channel
	// which are stuck due to insufficient funds
	ClientRetrieveTryRestartInsufficientFunds(ctx context.Context, paymentChannel address.Address) error //perm:write

	// ClientCancelRetrievalDeal cancels an ongoing retrieval deal based on DealID
	ClientCancelRetrievalDeal(ctx context.Context, dealid retrievalmarket.DealID) error //perm:write

	// ClientUnimport removes references to the specified file from filestore
	//ClientUnimport(path string)

	// ClientListImports lists imported files and their root CIDs
	ClientListImports(ctx context.Context) ([]Import, error) //perm:write

	//ClientListAsks() []Ask

	// MethodGroup: State
	// The State methods are used to query, inspect, and interact with chain state.
	// Most methods take a TipSetKey as a parameter. The state looked up is the parent state of the tipset.
	// A nil TipSetKey can be provided as a param, this will cause the heaviest tipset in the chain to be used.

	// StateCall runs the given message and returns its result without any persisted changes.
	//
	// StateCall applies the message to the tipset's parent state. The
	// message is not applied on-top-of the messages in the passed-in
	// tipset.
	StateCall(context.Context, *types.Message, types.TipSetKey) (*InvocResult, error) //perm:read
	// StateReplay replays a given message, assuming it was included in a block in the specified tipset.
	//
	// If a tipset key is provided, and a replacing message is found on chain,
	// the method will return an error saying that the message wasn't found
	//
	// If no tipset key is provided, the appropriate tipset is looked up, and if
	// the message was gas-repriced, the on-chain message will be replayed - in
	// that case the returned InvocResult.MsgCid will not match the Cid param
	//
	// If the caller wants to ensure that exactly the requested message was executed,
	// they MUST check that InvocResult.MsgCid is equal to the provided Cid.
	// Without this check both the requested and original message may appear as
	// successfully executed on-chain, which may look like a double-spend.
	//
	// A replacing message is a message with a different CID, any of Gas values, and
	// different signature, but with all other parameters matching (source/destination,
	// nonce, params, etc.)
	StateReplay(context.Context, types.TipSetKey, cid.Cid) (*InvocResult, error) //perm:read
	// StateGetActor returns the indicated actor's nonce and balance.
	// StateGetActor 返回指定参与者的随机数和余额。
	StateGetActor(ctx context.Context, actor address.Address, tsk types.TipSetKey) (*types.Actor, error) //perm:read
	// StateReadState returns the indicated actor's state.
	// StateReadState 返回指定角色的状态。
	StateReadState(ctx context.Context, actor address.Address, tsk types.TipSetKey) (*ActorState, error) //perm:read
	// StateListMessages looks back and returns all messages with a matching to or from address, stopping at the given height.
	// StateListMessages 回顾并返回所有具有匹配到或来自地址的消息，并在给定的高度处停止。
	StateListMessages(ctx context.Context, match *MessageMatch, tsk types.TipSetKey, toht abi.ChainEpoch) ([]cid.Cid, error) //perm:read
	// StateDecodeParams attempts to decode the provided params, based on the recipient actor address and method number.
	// StateDecodeParams 尝试根据接收者参与者地址和方法编号对提供的参数进行解码。
	StateDecodeParams(ctx context.Context, toAddr address.Address, method abi.MethodNum, params []byte, tsk types.TipSetKey) (interface{}, error) //perm:read

	// StateNetworkName returns the name of the network the node is synced to
	// StateNetworkName 返回节点同步到的网络名称
	StateNetworkName(context.Context) (dtypes.NetworkName, error) //perm:read
	// StateMinerSectors returns info about the given miner's sectors. If the filter bitfield is nil, all sectors are included.
	// StateMinerSectors 返回有关给定矿工扇区的信息。如果过滤器位域为零，则包括所有扇区。
	StateMinerSectors(context.Context, address.Address, *bitfield.BitField, types.TipSetKey) ([]*miner.SectorOnChainInfo, error) //perm:read
	// StateMinerActiveSectors returns info about sectors that a given miner is actively proving.
	// StateMinerActiveSectors 返回有关给定矿工正在积极证明的扇区的信息。
	StateMinerActiveSectors(context.Context, address.Address, types.TipSetKey) ([]*miner.SectorOnChainInfo, error) //perm:read
	// StateMinerProvingDeadline calculates the deadline at some epoch for a proving period
	// and returns the deadline-related calculations.
	// StateMinerProvingDeadline 计算证明期间某个时期的截止日期，并返回与截止日期相关的计算结果。
	StateMinerProvingDeadline(context.Context, address.Address, types.TipSetKey) (*dline.Info, error) //perm:read
	// StateMinerPower returns the power of the indicated miner
	// StateMinerPower 返回指定矿工的功率
	StateMinerPower(context.Context, address.Address, types.TipSetKey) (*MinerPower, error) //perm:read
	// StateMinerInfo returns info about the indicated miner
	// StateMinerInfo 返回有关指定矿工的信息
	StateMinerInfo(context.Context, address.Address, types.TipSetKey) (miner.MinerInfo, error) //perm:read
	// StateMinerDeadlines returns all the proving deadlines for the given miner
	// StateMinerDeadlines 返回给定矿工的所有证明截止日期
	StateMinerDeadlines(context.Context, address.Address, types.TipSetKey) ([]Deadline, error) //perm:read
	// StateMinerPartitions returns all partitions in the specified deadline
	// StateMinerPartitions 返回指定期限内的所有分区
	StateMinerPartitions(ctx context.Context, m address.Address, dlIdx uint64, tsk types.TipSetKey) ([]Partition, error) //perm:read
	// StateMinerFaults returns a bitfield indicating the faulty sectors of the given miner
	// StateMinerFaults 返回一个位域，指示给定矿工的故障扇区
	StateMinerFaults(context.Context, address.Address, types.TipSetKey) (bitfield.BitField, error) //perm:read
	// StateAllMinerFaults returns all non-expired Faults that occur within lookback epochs of the given tipset
	// StateAllMinerFaults 返回在给定提示集的回溯时期内发生的所有未过期故障
	StateAllMinerFaults(ctx context.Context, lookback abi.ChainEpoch, ts types.TipSetKey) ([]*Fault, error) //perm:read
	// StateMinerRecoveries returns a bitfield indicating the recovering sectors of the given miner
	// StateMinerRecoveries 返回一个位域，指示给定矿工的恢复扇区
	StateMinerRecoveries(context.Context, address.Address, types.TipSetKey) (bitfield.BitField, error) //perm:read
	// StateMinerInitialPledgeCollateral returns the precommit deposit for the specified miner's sector
	// StateMinerInitialPledgeCollat​​eral 返回指定矿工部门的预提交保证金
	StateMinerPreCommitDepositForPower(context.Context, address.Address, miner.SectorPreCommitInfo, types.TipSetKey) (types.BigInt, error) //perm:read
	// StateMinerInitialPledgeCollateral returns the initial pledge collateral for the specified miner's sector
	// StateMinerInitialPledgeCollat​​eral 返回指定矿工部门的初始质押抵押品
	StateMinerInitialPledgeCollateral(context.Context, address.Address, miner.SectorPreCommitInfo, types.TipSetKey) (types.BigInt, error) //perm:read
	// StateMinerAvailableBalance returns the portion of a miner's balance that can be withdrawn or spent
	// StateMinerAvailableBalance 返回矿工余额中可以提取或花费的部分
	StateMinerAvailableBalance(context.Context, address.Address, types.TipSetKey) (types.BigInt, error) //perm:read
	// StateMinerSectorAllocated checks if a sector is allocated
	// StateMinerSectorAllocated 检查扇区是否已分配
	StateMinerSectorAllocated(context.Context, address.Address, abi.SectorNumber, types.TipSetKey) (bool, error) //perm:read
	// StateSectorPreCommitInfo returns the PreCommit info for the specified miner's sector
	// StateSectorPreCommitInfo 返回指定矿工扇区的预提交信息
	StateSectorPreCommitInfo(context.Context, address.Address, abi.SectorNumber, types.TipSetKey) (miner.SectorPreCommitOnChainInfo, error) //perm:read
	// StateSectorGetInfo returns the on-chain info for the specified miner's sector. Returns null in case the sector info isn't found
	// NOTE: returned info.Expiration may not be accurate in some cases, use StateSectorExpiration to get accurate
	// expiration epoch
	// StateSectorGetInfo 返回指定矿工扇区的链上信息。如果找不到扇区信息，则返回 null
	// 注意：返回的 info.Expiration 在某些情况下可能不准确，请使用 StateSectorExpiration 获取准确的到期时间
	StateSectorGetInfo(context.Context, address.Address, abi.SectorNumber, types.TipSetKey) (*miner.SectorOnChainInfo, error) //perm:read
	// StateSectorExpiration returns epoch at which given sector will expire
	// StateSectorExpiration 返回给定扇区将到期的纪元
	StateSectorExpiration(context.Context, address.Address, abi.SectorNumber, types.TipSetKey) (*miner.SectorExpiration, error) //perm:read
	// StateSectorPartition finds deadline/partition with the specified sector
	// StateSectorPartition 查找具有指定扇区的截止日期/分区
	StateSectorPartition(ctx context.Context, maddr address.Address, sectorNumber abi.SectorNumber, tok types.TipSetKey) (*miner.SectorLocation, error) //perm:read
	// StateSearchMsg looks back up to limit epochs in the chain for a message, and returns its receipt and the tipset where it was executed
	//
	// NOTE: If a replacing message is found on chain, this method will return
	// a MsgLookup for the replacing message - the MsgLookup.Message will be a different
	// CID than the one provided in the 'cid' param, MsgLookup.Receipt will contain the
	// result of the execution of the replacing message.
	//
	// If the caller wants to ensure that exactly the requested message was executed,
	// they must check that MsgLookup.Message is equal to the provided 'cid', or set the
	// `allowReplaced` parameter to false. Without this check, and with `allowReplaced`
	// set to true, both the requested and original message may appear as
	// successfully executed on-chain, which may look like a double-spend.
	//
	// A replacing message is a message with a different CID, any of Gas values, and
	// different signature, but with all other parameters matching (source/destination,
	// nonce, params, etc.)
	// StateSearchMsg 回溯以限制消息链中的纪元，并返回其收据和执行它的提示集
	StateSearchMsg(ctx context.Context, from types.TipSetKey, msg cid.Cid, limit abi.ChainEpoch, allowReplaced bool) (*MsgLookup, error) //perm:read
	// StateWaitMsg looks back up to limit epochs in the chain for a message.
	// If not found, it blocks until the message arrives on chain, and gets to the
	// indicated confidence depth.
	//
	// NOTE: If a replacing message is found on chain, this method will return
	// a MsgLookup for the replacing message - the MsgLookup.Message will be a different
	// CID than the one provided in the 'cid' param, MsgLookup.Receipt will contain the
	// result of the execution of the replacing message.
	//
	// If the caller wants to ensure that exactly the requested message was executed,
	// they must check that MsgLookup.Message is equal to the provided 'cid', or set the
	// `allowReplaced` parameter to false. Without this check, and with `allowReplaced`
	// set to true, both the requested and original message may appear as
	// successfully executed on-chain, which may look like a double-spend.
	//
	// A replacing message is a message with a different CID, any of Gas values, and
	// different signature, but with all other parameters matching (source/destination,
	// nonce, params, etc.)
	// StateWaitMsg 回溯以限制消息链中的纪元。
	StateWaitMsg(ctx context.Context, cid cid.Cid, confidence uint64, limit abi.ChainEpoch, allowReplaced bool) (*MsgLookup, error) //perm:read
	// StateListMiners returns the addresses of every miner that has claimed power in the Power Actor
	// StateListMiners 返回每个在 Power Actor 中声称拥有权力的矿工的地址
	StateListMiners(context.Context, types.TipSetKey) ([]address.Address, error) //perm:read
	// StateListActors returns the addresses of every actor in the state
	// StateListActors 返回状态中每个参与者的地址
	StateListActors(context.Context, types.TipSetKey) ([]address.Address, error) //perm:read
	// StateMarketBalance looks up the Escrow and Locked balances of the given address in the Storage Market
	// StateMarketBalance 在存储市场中查找给定地址的托管和锁定余额
	StateMarketBalance(context.Context, address.Address, types.TipSetKey) (MarketBalance, error) //perm:read
	// StateMarketParticipants returns the Escrow and Locked balances of every participant in the Storage Market
	// StateMarketParticipants 返回存储市场中每个参与者的托管和锁定余额
	StateMarketParticipants(context.Context, types.TipSetKey) (map[string]MarketBalance, error) //perm:read
	// StateMarketDeals returns information about every deal in the Storage Market
	// StateMarketDeals 返回有关存储市场中每笔交易的信息
	StateMarketDeals(context.Context, types.TipSetKey) (map[string]MarketDeal, error) //perm:read
	// StateMarketStorageDeal returns information about the indicated deal
	// StateMarketStorageDeal 返回有关指定交易的信息
	StateMarketStorageDeal(context.Context, abi.DealID, types.TipSetKey) (*MarketDeal, error) //perm:read
	// StateLookupID retrieves the ID address of the given address
	// StateLookupID 检索给定地址的 ID 地址
	StateLookupID(context.Context, address.Address, types.TipSetKey) (address.Address, error) //perm:read
	// StateAccountKey returns the public key address of the given ID address
	// StateAccountKey 返回给定 ID 地址的公钥地址
	StateAccountKey(context.Context, address.Address, types.TipSetKey) (address.Address, error) //perm:read
	// StateChangedActors returns all the actors whose states change between the two given state CIDs
	// StateChangedActors 返回状态在两个给定状态 CID 之间发生变化的所有参与者
	// TODO: Should this take tipset keys instead?
	StateChangedActors(context.Context, cid.Cid, cid.Cid) (map[string]types.Actor, error) //perm:read
	// StateMinerSectorCount returns the number of sectors in a miner's sector set and proving set
	// StateMinerSectorCount 返回矿工扇区集和证明集中的扇区数
	StateMinerSectorCount(context.Context, address.Address, types.TipSetKey) (MinerSectors, error) //perm:read
	// StateCompute is a flexible command that applies the given messages on the given tipset.
	// The messages are run as though the VM were at the provided height.
	//
	// When called, StateCompute will:
	// - Load the provided tipset, or use the current chain head if not provided
	// - Compute the tipset state of the provided tipset on top of the parent state
	//   - (note that this step runs before vmheight is applied to the execution)
	//   - Execute state upgrade if any were scheduled at the epoch, or in null
	//     blocks preceding the tipset
	//   - Call the cron actor on null blocks preceding the tipset
	//   - For each block in the tipset
	//     - Apply messages in blocks in the specified
	//     - Award block reward by calling the reward actor
	//   - Call the cron actor for the current epoch
	// - If the specified vmheight is higher than the current epoch, apply any
	//   needed state upgrades to the state
	// - Apply the specified messages to the state
	//
	// The vmheight parameter sets VM execution epoch, and can be used to simulate
	// message execution in different network versions. If the specified vmheight
	// epoch is higher than the epoch of the specified tipset, any state upgrades
	// until the vmheight will be executed on the state before applying messages
	// specified by the user.
	//
	// Note that the initial tipset state computation is not affected by the
	// vmheight parameter - only the messages in the `apply` set are
	//
	// If the caller wants to simply compute the state, vmheight should be set to
	// the epoch of the specified tipset.
	//
	// Messages in the `apply` parameter must have the correct nonces, and gas
	// values set.
	// StateCompute 是一个灵活的命令，它在给定的提示集上应用给定的消息。
	StateCompute(context.Context, abi.ChainEpoch, []*types.Message, types.TipSetKey) (*ComputeStateOutput, error) //perm:read
	// StateVerifierStatus returns the data cap for the given address.
	// Returns nil if there is no entry in the data cap table for the
	// address.
	// StateVerifierStatus 返回给定地址的数据上限。
	// 如果地址的数据上限表中没有条目，则返回 nil。
	StateVerifierStatus(ctx context.Context, addr address.Address, tsk types.TipSetKey) (*abi.StoragePower, error) //perm:read
	// StateVerifiedClientStatus returns the data cap for the given address.
	// Returns nil if there is no entry in the data cap table for the
	// address.
	// StateVerifiedClientStatus 返回给定地址的数据上限。
	// 如果地址的数据上限表中没有条目，则返回 nil。
	StateVerifiedClientStatus(ctx context.Context, addr address.Address, tsk types.TipSetKey) (*abi.StoragePower, error) //perm:read
	// StateVerifiedClientStatus returns the address of the Verified Registry's root key
	// StateVerifiedClientStatus 返回已验证注册表的根密钥的地址
	StateVerifiedRegistryRootKey(ctx context.Context, tsk types.TipSetKey) (address.Address, error) //perm:read
	// StateDealProviderCollateralBounds returns the min and max collateral a storage provider
	// can issue. It takes the deal size and verified status as parameters.
	// StateDealProviderCollat​​eralBounds 返回存储提供商可以发行的最小和最大抵押品。它将交易规模和验证状态作为参数。
	StateDealProviderCollateralBounds(context.Context, abi.PaddedPieceSize, bool, types.TipSetKey) (DealCollateralBounds, error) //perm:read

	// StateCirculatingSupply returns the exact circulating supply of Filecoin at the given tipset.
	// This is not used anywhere in the protocol itself, and is only for external consumption.
	// StateCirculatingSupply 在给定的提示集上返回 Filecoin 的确切循环供应量。
	// 这不在协议本身的任何地方使用，仅用于外部消费。
	StateCirculatingSupply(context.Context, types.TipSetKey) (abi.TokenAmount, error) //perm:read
	// StateVMCirculatingSupplyInternal returns an approximation of the circulating supply of Filecoin at the given tipset.
	// This is the value reported by the runtime interface to actors code.
	// StateVMCirculatingSupplyInternal 返回给定提示集的 Filecoin 循环供应量的近似值。
	// 这是运行时接口向参与者代码报告的值。
	StateVMCirculatingSupplyInternal(context.Context, types.TipSetKey) (CirculatingSupply, error) //perm:read
	// StateNetworkVersion returns the network version at the given tipset
	// StateNetworkVersion 返回给定提示集的网络版本
	StateNetworkVersion(context.Context, types.TipSetKey) (apitypes.NetworkVersion, error) //perm:read

	// MethodGroup: Msig
	// The Msig methods are used to interact with multisig wallets on the
	// filecoin network

	// MsigGetAvailableBalance returns the portion of a multisig's balance that can be withdrawn or spent
	// MsigGetAvailableBalance 返回可以提取或花费的多重签名余额部分
	MsigGetAvailableBalance(context.Context, address.Address, types.TipSetKey) (types.BigInt, error) //perm:read
	// MsigGetVestingSchedule returns the vesting details of a given multisig.
	// MsigGetVestingSchedule 返回给定多重签名的归属细节。
	MsigGetVestingSchedule(context.Context, address.Address, types.TipSetKey) (MsigVesting, error) //perm:read
	// MsigGetVested returns the amount of FIL that vested in a multisig in a certain period.
	// It takes the following params: <multisig address>, <start epoch>, <end epoch>
	// MsigGetVested 返回特定时期内归属于多重签名的 FIL 数量。
	MsigGetVested(context.Context, address.Address, types.TipSetKey, types.TipSetKey) (types.BigInt, error) //perm:read

	//MsigGetPending returns pending transactions for the given multisig
	//wallet. Once pending transactions are fully approved, they will no longer
	//appear here.
	//MsigGetPending 返回给定多重签名钱包的待处理交易。一旦待处理交易获得完全批准，它们将不再出现在此处。
	MsigGetPending(context.Context, address.Address, types.TipSetKey) ([]*MsigTransaction, error) //perm:read

	// MsigCreate creates a multisig wallet
	// It takes the following params: <required number of senders>, <approving addresses>, <unlock duration>
	//<initial balance>, <sender address of the create msg>, <gas price>
	// MsigCreate 创建一个多重签名钱包
	MsigCreate(context.Context, uint64, []address.Address, abi.ChainEpoch, types.BigInt, address.Address, types.BigInt) (*MessagePrototype, error) //perm:sign

	// MsigPropose proposes a multisig message
	// It takes the following params: <multisig address>, <recipient address>, <value to transfer>,
	// <sender address of the propose msg>, <method to call in the proposed message>, <params to include in the proposed message>
	// MsigPropose 提出多签消息
	MsigPropose(context.Context, address.Address, address.Address, types.BigInt, address.Address, uint64, []byte) (*MessagePrototype, error) //perm:sign

	// MsigApprove approves a previously-proposed multisig message by transaction ID
	// It takes the following params: <multisig address>, <proposed transaction ID> <signer address>
	// MsigApprove 通过交易 ID 批准先前提议的多重签名消息
	MsigApprove(context.Context, address.Address, uint64, address.Address) (*MessagePrototype, error) //perm:sign

	// MsigApproveTxnHash approves a previously-proposed multisig message, specified
	// using both transaction ID and a hash of the parameters used in the
	// proposal. This method of approval can be used to ensure you only approve
	// exactly the transaction you think you are.
	// It takes the following params: <multisig address>, <proposed message ID>, <proposer address>, <recipient address>, <value to transfer>,
	// <sender address of the approve msg>, <method to call in the proposed message>, <params to include in the proposed message>
	// MsigApproveTxnHash 批准先前提议的多重签名消息，该消息使用交易 ID 和提议中使用的参数的哈希值指定。这种批准方法可用于确保您只批准您认为的交易。
	MsigApproveTxnHash(context.Context, address.Address, uint64, address.Address, address.Address, types.BigInt, address.Address, uint64, []byte) (*MessagePrototype, error) //perm:sign

	// MsigCancel cancels a previously-proposed multisig message
	// It takes the following params: <multisig address>, <proposed transaction ID>, <recipient address>, <value to transfer>,
	// <sender address of the cancel msg>, <method to call in the proposed message>, <params to include in the proposed message>
	// MsigCancel 取消先前提议的多签消息
	MsigCancel(context.Context, address.Address, uint64, address.Address, types.BigInt, address.Address, uint64, []byte) (*MessagePrototype, error) //perm:sign

	// MsigAddPropose proposes adding a signer in the multisig
	// It takes the following params: <multisig address>, <sender address of the propose msg>,
	// <new signer>, <whether the number of required signers should be increased>
	// MsigAddPropose 提议在多重签名中添加签名者
	MsigAddPropose(context.Context, address.Address, address.Address, address.Address, bool) (*MessagePrototype, error) //perm:sign

	// MsigAddApprove approves a previously proposed AddSigner message
	// It takes the following params: <multisig address>, <sender address of the approve msg>, <proposed message ID>,
	// <proposer address>, <new signer>, <whether the number of required signers should be increased>
	// MsigAddApprove 批准先前提议的 AddSigner 消息
	MsigAddApprove(context.Context, address.Address, address.Address, uint64, address.Address, address.Address, bool) (*MessagePrototype, error) //perm:sign

	// MsigAddCancel cancels a previously proposed AddSigner message
	// It takes the following params: <multisig address>, <sender address of the cancel msg>, <proposed message ID>,
	// <new signer>, <whether the number of required signers should be increased>
	// MsigAddCancel 取消先前提议的 AddSigner 消息
	MsigAddCancel(context.Context, address.Address, address.Address, uint64, address.Address, bool) (*MessagePrototype, error) //perm:sign

	// MsigSwapPropose proposes swapping 2 signers in the multisig
	// It takes the following params: <multisig address>, <sender address of the propose msg>,
	// <old signer>, <new signer>
	// MsigSwapPropose 提议在多重签名中交换 2 个签名者
	MsigSwapPropose(context.Context, address.Address, address.Address, address.Address, address.Address) (*MessagePrototype, error) //perm:sign

	// MsigSwapApprove approves a previously proposed SwapSigner
	// It takes the following params: <multisig address>, <sender address of the approve msg>, <proposed message ID>,
	// <proposer address>, <old signer>, <new signer>
	// MsigSwapApprove 批准先前提议的 SwapSigner
	MsigSwapApprove(context.Context, address.Address, address.Address, uint64, address.Address, address.Address, address.Address) (*MessagePrototype, error) //perm:sign

	// MsigSwapCancel cancels a previously proposed SwapSigner message
	// It takes the following params: <multisig address>, <sender address of the cancel msg>, <proposed message ID>,
	// <old signer>, <new signer>
	// MsigSwapCancel 取消先前提议的 SwapSigner 消息
	MsigSwapCancel(context.Context, address.Address, address.Address, uint64, address.Address, address.Address) (*MessagePrototype, error) //perm:sign

	// MsigRemoveSigner proposes the removal of a signer from the multisig.
	// It accepts the multisig to make the change on, the proposer address to
	// send the message from, the address to be removed, and a boolean
	// indicating whether or not the signing threshold should be lowered by one
	// along with the address removal.
	// MsigRemoveSigner 建议从多重签名中删除签名者。
	// 它接受多重签名以进行更改、发送消息的提议者地址、要删除的地址以及指示签名阈值是否应随地址删除一起降低的布尔值。
	MsigRemoveSigner(ctx context.Context, msig address.Address, proposer address.Address, toRemove address.Address, decrease bool) (*MessagePrototype, error) //perm:sign

	// MarketAddBalance adds funds to the market actor
	// MarketAddBalance 为市场参与者增加资金
	MarketAddBalance(ctx context.Context, wallet, addr address.Address, amt types.BigInt) (cid.Cid, error) //perm:sign
	// MarketGetReserved gets the amount of funds that are currently reserved for the address
	// MarketGetReserved 获取当前为地址预留的资金量
	MarketGetReserved(ctx context.Context, addr address.Address) (types.BigInt, error) //perm:sign
	// MarketReserveFunds reserves funds for a deal
	// MarketReserveFunds 为交易预留资金
	MarketReserveFunds(ctx context.Context, wallet address.Address, addr address.Address, amt types.BigInt) (cid.Cid, error) //perm:sign
	// MarketReleaseFunds releases funds reserved by MarketReserveFunds
	// MarketReleaseFunds 释放 MarketReserveFunds 预留的资金
	MarketReleaseFunds(ctx context.Context, addr address.Address, amt types.BigInt) error //perm:sign
	// MarketWithdraw withdraws unlocked funds from the market actor
	// MarketWithdraw 从市场参与者那里提取未锁定的资金
	MarketWithdraw(ctx context.Context, wallet, addr address.Address, amt types.BigInt) (cid.Cid, error) //perm:sign

	// MethodGroup: Paych
	// The Paych methods are for interacting with and managing payment channels

	PaychGet(ctx context.Context, from, to address.Address, amt types.BigInt) (*ChannelInfo, error)                     //perm:sign
	PaychGetWaitReady(context.Context, cid.Cid) (address.Address, error)                                                //perm:sign
	PaychAvailableFunds(ctx context.Context, ch address.Address) (*ChannelAvailableFunds, error)                        //perm:sign
	PaychAvailableFundsByFromTo(ctx context.Context, from, to address.Address) (*ChannelAvailableFunds, error)          //perm:sign
	PaychList(context.Context) ([]address.Address, error)                                                               //perm:read
	PaychStatus(context.Context, address.Address) (*PaychStatus, error)                                                 //perm:read
	PaychSettle(context.Context, address.Address) (cid.Cid, error)                                                      //perm:sign
	PaychCollect(context.Context, address.Address) (cid.Cid, error)                                                     //perm:sign
	PaychAllocateLane(ctx context.Context, ch address.Address) (uint64, error)                                          //perm:sign
	PaychNewPayment(ctx context.Context, from, to address.Address, vouchers []VoucherSpec) (*PaymentInfo, error)        //perm:sign
	PaychVoucherCheckValid(context.Context, address.Address, *paych.SignedVoucher) error                                //perm:read
	PaychVoucherCheckSpendable(context.Context, address.Address, *paych.SignedVoucher, []byte, []byte) (bool, error)    //perm:read
	PaychVoucherCreate(context.Context, address.Address, types.BigInt, uint64) (*VoucherCreateResult, error)            //perm:sign
	PaychVoucherAdd(context.Context, address.Address, *paych.SignedVoucher, []byte, types.BigInt) (types.BigInt, error) //perm:write
	PaychVoucherList(context.Context, address.Address) ([]*paych.SignedVoucher, error)                                  //perm:write
	PaychVoucherSubmit(context.Context, address.Address, *paych.SignedVoucher, []byte, []byte) (cid.Cid, error)         //perm:sign

	// MethodGroup: Node
	// These methods are general node management and status commands

	NodeStatus(ctx context.Context, inclChainStatus bool) (NodeStatus, error) //perm:read

	// CreateBackup creates node backup onder the specified file name. The
	// method requires that the lotus daemon is running with the
	// LOTUS_BACKUP_BASE_PATH environment variable set to some path, and that
	// the path specified when calling CreateBackup is within the base path
	CreateBackup(ctx context.Context, fpath string) error //perm:admin
}

type FileRef struct {
	Path  string
	IsCAR bool
}

type MinerSectors struct {
	// Live sectors that should be proven.
	Live uint64
	// Sectors actively contributing to power.
	Active uint64
	// Sectors with failed proofs.
	Faulty uint64
}

type ImportRes struct {
	Root     cid.Cid
	ImportID multistore.StoreID
}

type Import struct {
	Key multistore.StoreID
	Err string

	Root     *cid.Cid
	Source   string
	FilePath string
}

type DealInfo struct {
	ProposalCid cid.Cid
	State       storagemarket.StorageDealStatus
	Message     string // more information about deal state, particularly errors
	DealStages  *storagemarket.DealStages
	Provider    address.Address

	DataRef  *storagemarket.DataRef
	PieceCID cid.Cid
	Size     uint64

	PricePerEpoch types.BigInt
	Duration      uint64

	DealID abi.DealID

	CreationTime time.Time
	Verified     bool

	TransferChannelID *datatransfer.ChannelID
	DataTransfer      *DataTransferChannel
}

type MsgLookup struct {
	Message   cid.Cid // Can be different than requested, in case it was replaced, but only gas values changed
	Receipt   types.MessageReceipt
	ReturnDec interface{}
	TipSet    types.TipSetKey
	Height    abi.ChainEpoch
}

type MsgGasCost struct {
	Message            cid.Cid // Can be different than requested, in case it was replaced, but only gas values changed
	GasUsed            abi.TokenAmount
	BaseFeeBurn        abi.TokenAmount
	OverEstimationBurn abi.TokenAmount
	MinerPenalty       abi.TokenAmount
	MinerTip           abi.TokenAmount
	Refund             abi.TokenAmount
	TotalCost          abi.TokenAmount
}

// BlsMessages[x].cid = Cids[x]
// SecpkMessages[y].cid = Cids[BlsMessages.length + y]
type BlockMessages struct {
	BlsMessages   []*types.Message
	SecpkMessages []*types.SignedMessage

	Cids []cid.Cid
}

type Message struct {
	Cid     cid.Cid
	Message *types.Message
}

type ActorState struct {
	Balance types.BigInt
	Code    cid.Cid
	State   interface{}
}

type PCHDir int

const (
	PCHUndef PCHDir = iota
	PCHInbound
	PCHOutbound
)

type PaychStatus struct {
	ControlAddr address.Address
	Direction   PCHDir
}

type ChannelInfo struct {
	Channel      address.Address
	WaitSentinel cid.Cid
}

type ChannelAvailableFunds struct {
	// Channel is the address of the channel
	Channel *address.Address
	// From is the from address of the channel (channel creator)
	From address.Address
	// To is the to address of the channel
	To address.Address
	// ConfirmedAmt is the amount of funds that have been confirmed on-chain
	// for the channel
	ConfirmedAmt types.BigInt
	// PendingAmt is the amount of funds that are pending confirmation on-chain
	PendingAmt types.BigInt
	// PendingWaitSentinel can be used with PaychGetWaitReady to wait for
	// confirmation of pending funds
	PendingWaitSentinel *cid.Cid
	// QueuedAmt is the amount that is queued up behind a pending request
	QueuedAmt types.BigInt
	// VoucherRedeemedAmt is the amount that is redeemed by vouchers on-chain
	// and in the local datastore
	VoucherReedeemedAmt types.BigInt
}

type PaymentInfo struct {
	Channel      address.Address
	WaitSentinel cid.Cid
	Vouchers     []*paych.SignedVoucher
}

type VoucherSpec struct {
	Amount      types.BigInt
	TimeLockMin abi.ChainEpoch
	TimeLockMax abi.ChainEpoch
	MinSettle   abi.ChainEpoch

	Extra *paych.ModVerifyParams
}

// VoucherCreateResult is the response to calling PaychVoucherCreate
type VoucherCreateResult struct {
	// Voucher that was created, or nil if there was an error or if there
	// were insufficient funds in the channel
	Voucher *paych.SignedVoucher
	// Shortfall is the additional amount that would be needed in the channel
	// in order to be able to create the voucher
	Shortfall types.BigInt
}

type MinerPower struct {
	MinerPower  power.Claim
	TotalPower  power.Claim
	HasMinPower bool
}

type QueryOffer struct {
	Err string

	Root  cid.Cid
	Piece *cid.Cid

	Size                    uint64
	MinPrice                types.BigInt
	UnsealPrice             types.BigInt
	PaymentInterval         uint64
	PaymentIntervalIncrease uint64
	Miner                   address.Address
	MinerPeer               retrievalmarket.RetrievalPeer
}

func (o *QueryOffer) Order(client address.Address) RetrievalOrder {
	return RetrievalOrder{
		Root:                    o.Root,
		Piece:                   o.Piece,
		Size:                    o.Size,
		Total:                   o.MinPrice,
		UnsealPrice:             o.UnsealPrice,
		PaymentInterval:         o.PaymentInterval,
		PaymentIntervalIncrease: o.PaymentIntervalIncrease,
		Client:                  client,

		Miner:     o.Miner,
		MinerPeer: &o.MinerPeer,
	}
}

type MarketBalance struct {
	Escrow big.Int
	Locked big.Int
}

// 市场交易
type MarketDeal struct {
	Proposal market.DealProposal	// 交易提案
	State    market.DealState		// 交易状态
}

type RetrievalOrder struct {
	// TODO: make this less unixfs specific
	Root  cid.Cid
	Piece *cid.Cid
	Size  uint64

	LocalStore *multistore.StoreID // if specified, get data from local store
	// TODO: support offset
	Total                   types.BigInt
	UnsealPrice             types.BigInt
	PaymentInterval         uint64
	PaymentIntervalIncrease uint64
	Client                  address.Address
	Miner                   address.Address
	MinerPeer               *retrievalmarket.RetrievalPeer
}

type InvocResult struct {
	MsgCid         cid.Cid
	Msg            *types.Message
	MsgRct         *types.MessageReceipt
	GasCost        MsgGasCost
	ExecutionTrace types.ExecutionTrace
	Error          string
	Duration       time.Duration
}

type MethodCall struct {
	types.MessageReceipt
	Error string
}

type StartDealParams struct {
	Data               *storagemarket.DataRef
	Wallet             address.Address
	Miner              address.Address
	EpochPrice         types.BigInt
	MinBlocksDuration  uint64
	ProviderCollateral big.Int
	DealStartEpoch     abi.ChainEpoch
	FastRetrieval      bool
	VerifiedDeal       bool
}

func (s *StartDealParams) UnmarshalJSON(raw []byte) (err error) {
	type sdpAlias StartDealParams

	sdp := sdpAlias{
		FastRetrieval: true,
	}

	if err := json.Unmarshal(raw, &sdp); err != nil {
		return err
	}

	*s = StartDealParams(sdp)

	return nil
}

type IpldObject struct {
	Cid cid.Cid
	Obj interface{}
}

type ActiveSync struct {
	WorkerID uint64
	Base     *types.TipSet
	Target   *types.TipSet

	Stage  SyncStateStage
	Height abi.ChainEpoch

	Start   time.Time
	End     time.Time
	Message string
}

type SyncState struct {
	ActiveSyncs []ActiveSync

	VMApplied uint64
}

type SyncStateStage int

const (
	StageIdle = SyncStateStage(iota)
	StageHeaders
	StagePersistHeaders
	StageMessages
	StageSyncComplete
	StageSyncErrored
	StageFetchingMessages
)

func (v SyncStateStage) String() string {
	switch v {
	case StageIdle:
		return "idle"
	case StageHeaders:
		return "header sync"
	case StagePersistHeaders:
		return "persisting headers"
	case StageMessages:
		return "message sync"
	case StageSyncComplete:
		return "complete"
	case StageSyncErrored:
		return "error"
	case StageFetchingMessages:
		return "fetching messages"
	default:
		return fmt.Sprintf("<unknown: %d>", v)
	}
}

type MpoolChange int

const (
	MpoolAdd MpoolChange = iota
	MpoolRemove
)

type MpoolUpdate struct {
	Type    MpoolChange
	Message *types.SignedMessage
}

type ComputeStateOutput struct {
	Root  cid.Cid
	Trace []*InvocResult
}

type DealCollateralBounds struct {
	Min abi.TokenAmount
	Max abi.TokenAmount
}

type CirculatingSupply struct {
	FilVested           abi.TokenAmount
	FilMined            abi.TokenAmount
	FilBurnt            abi.TokenAmount
	FilLocked           abi.TokenAmount
	FilCirculating      abi.TokenAmount
	FilReserveDisbursed abi.TokenAmount
}

type MiningBaseInfo struct {
	MinerPower        types.BigInt
	NetworkPower      types.BigInt
	Sectors           []builtin.SectorInfo
	WorkerKey         address.Address
	SectorSize        abi.SectorSize
	PrevBeaconEntry   types.BeaconEntry
	BeaconEntries     []types.BeaconEntry
	EligibleForMining bool
}

type BlockTemplate struct {
	Miner            address.Address
	Parents          types.TipSetKey
	Ticket           *types.Ticket
	Eproof           *types.ElectionProof
	BeaconValues     []types.BeaconEntry
	Messages         []*types.SignedMessage
	Epoch            abi.ChainEpoch
	Timestamp        uint64
	WinningPoStProof []builtin.PoStProof
}

type DataSize struct {
	PayloadSize int64
	PieceSize   abi.PaddedPieceSize
}

type DataCIDSize struct {
	PayloadSize int64
	PieceSize   abi.PaddedPieceSize
	PieceCID    cid.Cid
}

type CommPRet struct {
	Root cid.Cid
	Size abi.UnpaddedPieceSize
}
type HeadChange struct {
	Type string
	Val  *types.TipSet
}

type MsigProposeResponse int

const (
	MsigApprove MsigProposeResponse = iota
	MsigCancel
)

type Deadline struct {
	PostSubmissions      bitfield.BitField
	DisputableProofCount uint64
}

type Partition struct {
	AllSectors        bitfield.BitField		// 所有扇区
	FaultySectors     bitfield.BitField		// 故障扇区
	RecoveringSectors bitfield.BitField		// 回收扇区
	LiveSectors       bitfield.BitField		// 直播扇区
	ActiveSectors     bitfield.BitField		// 活跃扇区
}

type Fault struct {
	Miner address.Address
	Epoch abi.ChainEpoch
}

var EmptyVesting = MsigVesting{
	InitialBalance: types.EmptyInt,
	StartEpoch:     -1,
	UnlockDuration: -1,
}

type MsigVesting struct {
	InitialBalance abi.TokenAmount
	StartEpoch     abi.ChainEpoch
	UnlockDuration abi.ChainEpoch
}

type MessageMatch struct {
	To   address.Address
	From address.Address
}

type MsigTransaction struct {
	ID     int64
	To     address.Address
	Value  abi.TokenAmount
	Method abi.MethodNum
	Params []byte

	Approved []address.Address
}
