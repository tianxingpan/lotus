package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-jsonrpc"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/chain/stmgr"
	types "github.com/filecoin-project/lotus/chain/types"
	cid "github.com/ipfs/go-cid"
	cbg "github.com/whyrusleeping/cbor-gen"
	"golang.org/x/xerrors"
)

//go:generate go run github.com/golang/mock/mockgen -destination=servicesmock_test.go -package=cli -self_package github.com/filecoin-project/lotus/cli . ServicesAPI

// 服务API
type ServicesAPI interface {
	FullNodeAPI() api.FullNode

	GetBaseFee(ctx context.Context) (abi.TokenAmount, error)

	// MessageForSend creates a prototype of a message based on SendParams
	MessageForSend(ctx context.Context, params SendParams) (*api.MessagePrototype, error)

	// DecodeTypedParamsFromJSON takes in information needed to identify a method and converts JSON
	// parameters to bytes of their CBOR encoding
	DecodeTypedParamsFromJSON(ctx context.Context, to address.Address, method abi.MethodNum, paramstr string) ([]byte, error)

	RunChecksForPrototype(ctx context.Context, prototype *api.MessagePrototype) ([][]api.MessageCheckStatus, error)

	// PublishMessage takes in a message prototype and publishes it
	// before publishing the message, it runs checks on the node, message and mpool to verify that
	// message is valid and won't be stuck.
	// if `force` is true, it skips the checks
	PublishMessage(ctx context.Context, prototype *api.MessagePrototype, force bool) (*types.SignedMessage, [][]api.MessageCheckStatus, error)

	LocalAddresses(ctx context.Context) (address.Address, []address.Address, error)

	MpoolPendingFilter(ctx context.Context, filter func(*types.SignedMessage) bool, tsk types.TipSetKey) ([]*types.SignedMessage, error)
	MpoolCheckPendingMessages(ctx context.Context, a address.Address) ([][]api.MessageCheckStatus, error)

	// Close ends the session of services and disconnects from RPC, using Services after Close is called
	// most likely will result in an error
	// Should not be called concurrently
	Close() error
}

// 服务实现服务实现
type ServicesImpl struct {
	api    api.FullNode
	closer jsonrpc.ClientCloser
}

// 全节点API
func (s *ServicesImpl) FullNodeAPI() api.FullNode {
	return s.api
}

func (s *ServicesImpl) Close() error {
	if s.closer == nil {
		return xerrors.Errorf("Services already closed")
	}
	s.closer()
	s.closer = nil
	return nil
}

// 获取基础费用
func (s *ServicesImpl) GetBaseFee(ctx context.Context) (abi.TokenAmount, error) {
	// not used but useful

	ts, err := s.api.ChainHead(ctx)
	if err != nil {
		return big.Zero(), xerrors.Errorf("getting head: %w", err)
	}
	return ts.MinTicketBlock().ParentBaseFee, nil
}

// 从 JSON 解码类型化参数
func (s *ServicesImpl) DecodeTypedParamsFromJSON(ctx context.Context, to address.Address, method abi.MethodNum, paramstr string) ([]byte, error) {
	act, err := s.api.StateGetActor(ctx, to, types.EmptyTSK)
	if err != nil {
		return nil, err
	}

	methodMeta, found := stmgr.MethodsMap[act.Code][method]
	if !found {
		return nil, fmt.Errorf("method %d not found on actor %s", method, act.Code)
	}

	p := reflect.New(methodMeta.Params.Elem()).Interface().(cbg.CBORMarshaler)

	if err := json.Unmarshal([]byte(paramstr), p); err != nil {
		return nil, fmt.Errorf("unmarshaling input into params type: %w", err)
	}

	buf := new(bytes.Buffer)
	if err := p.MarshalCBOR(buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// 检查信息
type CheckInfo struct {
	MessageTie        cid.Cid	// 留言板
	CurrentMessageTie bool

	Check api.MessageCheckStatus
}

// 检查失败
var ErrCheckFailed = fmt.Errorf("check has failed")

// 运行原型检查
func (s *ServicesImpl) RunChecksForPrototype(ctx context.Context, prototype *api.MessagePrototype) ([][]api.MessageCheckStatus, error) {
	var outChecks [][]api.MessageCheckStatus
	checks, err := s.api.MpoolCheckMessages(ctx, []*api.MessagePrototype{prototype})
	if err != nil {
		return nil, xerrors.Errorf("message check: %w", err)
	}
	outChecks = append(outChecks, checks...)

	checks, err = s.api.MpoolCheckPendingMessages(ctx, prototype.Message.From)
	if err != nil {
		return nil, xerrors.Errorf("pending mpool check: %w", err)
	}
	outChecks = append(outChecks, checks...)

	return outChecks, nil
}

// PublishMessage modifies prototype to include gas estimation
// Errors with ErrCheckFailed if any of the checks fail
// First group of checks is related to the message prototype
// 发布消息
func (s *ServicesImpl) PublishMessage(ctx context.Context,
	prototype *api.MessagePrototype, force bool) (*types.SignedMessage, [][]api.MessageCheckStatus, error) {

	gasedMsg, err := s.api.GasEstimateMessageGas(ctx, &prototype.Message, nil, types.EmptyTSK)
	if err != nil {
		return nil, nil, xerrors.Errorf("estimating gas: %w", err)
	}
	prototype.Message = *gasedMsg

	if !force {
		checks, err := s.RunChecksForPrototype(ctx, prototype)
		if err != nil {
			return nil, nil, xerrors.Errorf("running checks: %w", err)
		}
		for _, chks := range checks {
			for _, c := range chks {
				if !c.OK {
					return nil, checks, ErrCheckFailed
				}
			}
		}
	}

	if prototype.ValidNonce {
		sm, err := s.api.WalletSignMessage(ctx, prototype.Message.From, &prototype.Message)
		if err != nil {
			return nil, nil, err
		}

		_, err = s.api.MpoolPush(ctx, sm)
		if err != nil {
			return nil, nil, err
		}
		return sm, nil, nil
	}

	sm, err := s.api.MpoolPushMessage(ctx, &prototype.Message, nil)
	if err != nil {
		return nil, nil, err
	}

	return sm, nil, nil
}

// 发送参数
type SendParams struct {
	To   address.Address
	From address.Address
	Val  abi.TokenAmount

	GasPremium *abi.TokenAmount
	GasFeeCap  *abi.TokenAmount
	GasLimit   *int64

	Nonce  *uint64
	Method abi.MethodNum
	Params []byte
}

// 发送消息
func (s *ServicesImpl) MessageForSend(ctx context.Context, params SendParams) (*api.MessagePrototype, error) {
	if params.From == address.Undef {
		defaddr, err := s.api.WalletDefaultAddress(ctx)
		if err != nil {
			return nil, err
		}
		params.From = defaddr
	}

	msg := types.Message{
		From:  params.From,
		To:    params.To,
		Value: params.Val,

		Method: params.Method,
		Params: params.Params,
	}

	if params.GasPremium != nil {
		msg.GasPremium = *params.GasPremium
	} else {
		msg.GasPremium = types.NewInt(0)
	}
	if params.GasFeeCap != nil {
		msg.GasFeeCap = *params.GasFeeCap
	} else {
		msg.GasFeeCap = types.NewInt(0)
	}
	if params.GasLimit != nil {
		msg.GasLimit = *params.GasLimit
	} else {
		msg.GasLimit = 0
	}
	validNonce := false
	if params.Nonce != nil {
		msg.Nonce = *params.Nonce
		validNonce = true
	}

	prototype := &api.MessagePrototype{
		Message:    msg,
		ValidNonce: validNonce,
	}
	return prototype, nil
}

// Mpool待处理过滤器
func (s *ServicesImpl) MpoolPendingFilter(ctx context.Context, filter func(*types.SignedMessage) bool,
	tsk types.TipSetKey) ([]*types.SignedMessage, error) {
	msgs, err := s.api.MpoolPending(ctx, types.EmptyTSK)
	if err != nil {
		return nil, xerrors.Errorf("getting pending messages: %w", err)
	}
	out := []*types.SignedMessage{}
	for _, sm := range msgs {
		if filter(sm) {
			out = append(out, sm)
		}
	}

	return out, nil
}

// 本地地址
func (s *ServicesImpl) LocalAddresses(ctx context.Context) (address.Address, []address.Address, error) {
	def, err := s.api.WalletDefaultAddress(ctx)
	if err != nil {
		return address.Undef, nil, xerrors.Errorf("getting default addr: %w", err)
	}

	all, err := s.api.WalletList(ctx)
	if err != nil {
		return address.Undef, nil, xerrors.Errorf("getting list of addrs: %w", err)
	}

	return def, all, nil
}

// Mpool检查待处理消息
func (s *ServicesImpl) MpoolCheckPendingMessages(ctx context.Context, a address.Address) ([][]api.MessageCheckStatus, error) {
	checks, err := s.api.MpoolCheckPendingMessages(ctx, a)
	if err != nil {
		return nil, xerrors.Errorf("pending mpool check: %w", err)
	}
	return checks, nil
}
