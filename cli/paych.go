package cli

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/filecoin-project/lotus/api"

	"github.com/filecoin-project/lotus/paychmgr"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/build"
	"github.com/urfave/cli/v2"

	"github.com/filecoin-project/lotus/chain/actors/builtin/paych"
	"github.com/filecoin-project/lotus/chain/types"
)

// 命令：管理支付渠道
var paychCmd = &cli.Command{
	Name:  "paych",
	Usage: "Manage payment channels",
	Subcommands: []*cli.Command{
		paychAddFundsCmd,
		paychListCmd,
		paychVoucherCmd,
		paychSettleCmd,
		paychStatusCmd,
		paychStatusByFromToCmd,
		paychCloseCmd,
	},
}

// 子命令： 向 fromAddress 和 toAddress 之间的支付通道添加资金。如果支付渠道尚不存在，则创建支付渠道。
var paychAddFundsCmd = &cli.Command{
	Name:      "add-funds",
	Usage:     "Add funds to the payment channel between fromAddress and toAddress. Creates the payment channel if it doesn't already exist.",
	ArgsUsage: "[fromAddress toAddress amount]",
	Flags: []cli.Flag{

		&cli.BoolFlag{
			Name:  "restart-retrievals",
			Usage: "restart stalled retrieval deals on this payment channel",
			Value: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 3 {
			return ShowHelp(cctx, fmt.Errorf("must pass three arguments: <from> <to> <available funds>"))
		}

		from, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return ShowHelp(cctx, fmt.Errorf("failed to parse from address: %s", err))
		}

		to, err := address.NewFromString(cctx.Args().Get(1))
		if err != nil {
			return ShowHelp(cctx, fmt.Errorf("failed to parse to address: %s", err))
		}

		amt, err := types.ParseFIL(cctx.Args().Get(2))
		if err != nil {
			return ShowHelp(cctx, fmt.Errorf("parsing amount failed: %s", err))
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		// Send a message to chain to create channel / add funds to existing
		// channel
		info, err := api.PaychGet(ctx, from, to, types.BigInt(amt))
		if err != nil {
			return err
		}

		// Wait for the message to be confirmed
		chAddr, err := api.PaychGetWaitReady(ctx, info.WaitSentinel)
		if err != nil {
			return err
		}

		_, _ = fmt.Fprintln(cctx.App.Writer, chAddr)
		restartRetrievals := cctx.Bool("restart-retrievals")
		if restartRetrievals {
			return api.ClientRetrieveTryRestartInsufficientFunds(ctx, chAddr)
		}
		return nil
	},
}

// 子命令：按发件人/收件人地址显示活动出站支付渠道的状态
var paychStatusByFromToCmd = &cli.Command{
	Name:      "status-by-from-to",
	Usage:     "Show the status of an active outbound payment channel by from/to addresses",
	ArgsUsage: "[fromAddress toAddress]",
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 2 {
			return ShowHelp(cctx, fmt.Errorf("must pass two arguments: <from address> <to address>"))
		}
		ctx := ReqContext(cctx)

		from, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return ShowHelp(cctx, fmt.Errorf("failed to parse from address: %s", err))
		}

		to, err := address.NewFromString(cctx.Args().Get(1))
		if err != nil {
			return ShowHelp(cctx, fmt.Errorf("failed to parse to address: %s", err))
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		avail, err := api.PaychAvailableFundsByFromTo(ctx, from, to)
		if err != nil {
			return err
		}

		paychStatus(cctx.App.Writer, avail)
		return nil
	},
}

// 子命令：显示出境支付渠道的状态
var paychStatusCmd = &cli.Command{
	Name:      "status",
	Usage:     "Show the status of an outbound payment channel",
	ArgsUsage: "[channelAddress]",
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 1 {
			return ShowHelp(cctx, fmt.Errorf("must pass an argument: <channel address>"))
		}
		ctx := ReqContext(cctx)

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return ShowHelp(cctx, fmt.Errorf("failed to parse channel address: %s", err))
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		avail, err := api.PaychAvailableFunds(ctx, ch)
		if err != nil {
			return err
		}

		paychStatus(cctx.App.Writer, avail)
		return nil
	},
}

// 支付状态
func paychStatus(writer io.Writer, avail *api.ChannelAvailableFunds) {
	if avail.Channel == nil {
		if avail.PendingWaitSentinel != nil {
			_, _ = fmt.Fprint(writer, "Creating channel\n")
			_, _ = fmt.Fprintf(writer, "  From:          %s\n", avail.From)
			_, _ = fmt.Fprintf(writer, "  To:            %s\n", avail.To)
			_, _ = fmt.Fprintf(writer, "  Pending Amt:   %d\n", avail.PendingAmt)
			_, _ = fmt.Fprintf(writer, "  Wait Sentinel: %s\n", avail.PendingWaitSentinel)
			return
		}
		_, _ = fmt.Fprint(writer, "Channel does not exist\n")
		_, _ = fmt.Fprintf(writer, "  From: %s\n", avail.From)
		_, _ = fmt.Fprintf(writer, "  To:   %s\n", avail.To)
		return
	}

	if avail.PendingWaitSentinel != nil {
		_, _ = fmt.Fprint(writer, "Adding Funds to channel\n")
	} else {
		_, _ = fmt.Fprint(writer, "Channel exists\n")
	}

	nameValues := [][]string{
		{"Channel", avail.Channel.String()},
		{"From", avail.From.String()},
		{"To", avail.To.String()},
		{"Confirmed Amt", fmt.Sprintf("%d", avail.ConfirmedAmt)},
		{"Pending Amt", fmt.Sprintf("%d", avail.PendingAmt)},
		{"Queued Amt", fmt.Sprintf("%d", avail.QueuedAmt)},
		{"Voucher Redeemed Amt", fmt.Sprintf("%d", avail.VoucherReedeemedAmt)},
	}
	if avail.PendingWaitSentinel != nil {
		nameValues = append(nameValues, []string{
			"Add Funds Wait Sentinel",
			avail.PendingWaitSentinel.String(),
		})
	}
	_, _ = fmt.Fprint(writer, formatNameValues(nameValues))
}

// 格式名称值
func formatNameValues(nameValues [][]string) string {
	maxLen := 0
	for _, nv := range nameValues {
		if len(nv[0]) > maxLen {
			maxLen = len(nv[0])
		}
	}
	out := make([]string, len(nameValues))
	for i, nv := range nameValues {
		namePad := strings.Repeat(" ", maxLen-len(nv[0]))
		out[i] = "  " + nv[0] + ": " + namePad + nv[1]
	}
	return strings.Join(out, "\n") + "\n"
}

// 子命令：列出所有本地注册的支付渠道
var paychListCmd = &cli.Command{
	Name:  "list",
	Usage: "List all locally registered payment channels",
	Action: func(cctx *cli.Context) error {
		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		chs, err := api.PaychList(ctx)
		if err != nil {
			return err
		}

		for _, v := range chs {
			_, _ = fmt.Fprintln(cctx.App.Writer, v.String())
		}
		return nil
	},
}

// 子命令：结算支付渠道
var paychSettleCmd = &cli.Command{
	Name:      "settle",
	Usage:     "Settle a payment channel",
	ArgsUsage: "[channelAddress]",
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 1 {
			return fmt.Errorf("must pass payment channel address")
		}

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return fmt.Errorf("failed to parse payment channel address: %s", err)
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		mcid, err := api.PaychSettle(ctx, ch)
		if err != nil {
			return err
		}

		mwait, err := api.StateWaitMsg(ctx, mcid, build.MessageConfidence)
		if err != nil {
			return nil
		}
		if mwait.Receipt.ExitCode != 0 {
			return fmt.Errorf("settle message execution failed (exit code %d)", mwait.Receipt.ExitCode)
		}

		_, _ = fmt.Fprintf(cctx.App.Writer, "Settled channel %s\n", ch)
		return nil
	},
}

// 子命令：为支付渠道筹集资金
var paychCloseCmd = &cli.Command{
	Name:      "collect",
	Usage:     "Collect funds for a payment channel",
	ArgsUsage: "[channelAddress]",
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 1 {
			return fmt.Errorf("must pass payment channel address")
		}

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return fmt.Errorf("failed to parse payment channel address: %s", err)
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		mcid, err := api.PaychCollect(ctx, ch)
		if err != nil {
			return err
		}

		mwait, err := api.StateWaitMsg(ctx, mcid, build.MessageConfidence)
		if err != nil {
			return nil
		}
		if mwait.Receipt.ExitCode != 0 {
			return fmt.Errorf("collect message execution failed (exit code %d)", mwait.Receipt.ExitCode)
		}

		_, _ = fmt.Fprintf(cctx.App.Writer, "Collected funds for channel %s\n", ch)
		return nil
	},
}

// 子命令：与支付渠道凭证互动
var paychVoucherCmd = &cli.Command{
	Name:  "voucher",
	Usage: "Interact with payment channel vouchers",
	Subcommands: []*cli.Command{
		paychVoucherCreateCmd,
		paychVoucherCheckCmd,
		paychVoucherAddCmd,
		paychVoucherListCmd,
		paychVoucherBestSpendableCmd,
		paychVoucherSubmitCmd,
	},
}

// 子命令：创建已签名的支付渠道凭证
var paychVoucherCreateCmd = &cli.Command{
	Name:      "create",
	Usage:     "Create a signed payment channel voucher",
	ArgsUsage: "[channelAddress amount]",
	Flags: []cli.Flag{
		&cli.IntFlag{
			Name:  "lane",
			Value: 0,
			Usage: "specify payment channel lane to use",
		},
	},
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 2 {
			return ShowHelp(cctx, fmt.Errorf("must pass two arguments: <channel> <amount>"))
		}

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return err
		}

		amt, err := types.ParseFIL(cctx.Args().Get(1))
		if err != nil {
			return ShowHelp(cctx, fmt.Errorf("parsing amount failed: %s", err))
		}

		lane := cctx.Int("lane")

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		v, err := api.PaychVoucherCreate(ctx, ch, types.BigInt(amt), uint64(lane))
		if err != nil {
			return err
		}

		if v.Voucher == nil {
			return fmt.Errorf("Could not create voucher: insufficient funds in channel, shortfall: %d", v.Shortfall)
		}

		enc, err := EncodedString(v.Voucher)
		if err != nil {
			return err
		}

		_, _ = fmt.Fprintln(cctx.App.Writer, enc)
		return nil
	},
}

// 子命令：检查支付渠道凭证的有效性
var paychVoucherCheckCmd = &cli.Command{
	Name:      "check",
	Usage:     "Check validity of payment channel voucher",
	ArgsUsage: "[channelAddress voucher]",
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 2 {
			return ShowHelp(cctx, fmt.Errorf("must pass payment channel address and voucher to validate"))
		}

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return err
		}

		sv, err := paych.DecodeSignedVoucher(cctx.Args().Get(1))
		if err != nil {
			return err
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		if err := api.PaychVoucherCheckValid(ctx, ch, sv); err != nil {
			return err
		}

		_, _ = fmt.Fprintln(cctx.App.Writer, "voucher is valid")
		return nil
	},
}

// 子命令：将支付渠道凭证添加到本地数据存储
var paychVoucherAddCmd = &cli.Command{
	Name:      "add",
	Usage:     "Add payment channel voucher to local datastore",
	ArgsUsage: "[channelAddress voucher]",
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 2 {
			return ShowHelp(cctx, fmt.Errorf("must pass payment channel address and voucher"))
		}

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return err
		}

		sv, err := paych.DecodeSignedVoucher(cctx.Args().Get(1))
		if err != nil {
			return err
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		// TODO: allow passing proof bytes
		if _, err := api.PaychVoucherAdd(ctx, ch, sv, nil, types.NewInt(0)); err != nil {
			return err
		}

		return nil
	},
}

// 子命令：列出给定支付渠道的存储凭证
var paychVoucherListCmd = &cli.Command{
	Name:      "list",
	Usage:     "List stored vouchers for a given payment channel",
	ArgsUsage: "[channelAddress]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "export",
			Usage: "Print voucher as serialized string",
		},
	},
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 1 {
			return ShowHelp(cctx, fmt.Errorf("must pass payment channel address"))
		}

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return err
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		vouchers, err := api.PaychVoucherList(ctx, ch)
		if err != nil {
			return err
		}

		for _, v := range sortVouchers(vouchers) {
			export := cctx.Bool("export")
			err := outputVoucher(cctx.App.Writer, v, export)
			if err != nil {
				return err
			}
		}

		return nil
	},
}

// 子命令：打印每个通道当前可花费的最高价值的代金券
var paychVoucherBestSpendableCmd = &cli.Command{
	Name:      "best-spendable",
	Usage:     "Print vouchers with highest value that is currently spendable for each lane",
	ArgsUsage: "[channelAddress]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "export",
			Usage: "Print voucher as serialized string",
		},
	},
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 1 {
			return ShowHelp(cctx, fmt.Errorf("must pass payment channel address"))
		}

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return err
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		vouchersByLane, err := paychmgr.BestSpendableByLane(ctx, api, ch)
		if err != nil {
			return err
		}

		var vouchers []*paych.SignedVoucher
		for _, vchr := range vouchersByLane {
			vouchers = append(vouchers, vchr)
		}
		for _, best := range sortVouchers(vouchers) {
			export := cctx.Bool("export")
			err := outputVoucher(cctx.App.Writer, best, export)
			if err != nil {
				return err
			}
		}

		return nil
	},
}

// 排序凭证
func sortVouchers(vouchers []*paych.SignedVoucher) []*paych.SignedVoucher {
	sort.Slice(vouchers, func(i, j int) bool {
		if vouchers[i].Lane == vouchers[j].Lane {
			return vouchers[i].Nonce < vouchers[j].Nonce
		}
		return vouchers[i].Lane < vouchers[j].Lane
	})
	return vouchers
}

// 输出凭证
func outputVoucher(w io.Writer, v *paych.SignedVoucher, export bool) error {
	var enc string
	if export {
		var err error
		enc, err = EncodedString(v)
		if err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(w, "Lane %d, Nonce %d: %s", v.Lane, v.Nonce, v.Amount.String())
	if export {
		_, _ = fmt.Fprintf(w, "; %s", enc)
	}
	_, _ = fmt.Fprintln(w)
	return nil
}

// 子命令：提交凭证到链更新支付通道状态
var paychVoucherSubmitCmd = &cli.Command{
	Name:      "submit",
	Usage:     "Submit voucher to chain to update payment channel state",
	ArgsUsage: "[channelAddress voucher]",
	Action: func(cctx *cli.Context) error {
		if cctx.Args().Len() != 2 {
			return ShowHelp(cctx, fmt.Errorf("must pass payment channel address and voucher"))
		}

		ch, err := address.NewFromString(cctx.Args().Get(0))
		if err != nil {
			return err
		}

		sv, err := paych.DecodeSignedVoucher(cctx.Args().Get(1))
		if err != nil {
			return err
		}

		api, closer, err := GetFullNodeAPI(cctx)
		if err != nil {
			return err
		}
		defer closer()

		ctx := ReqContext(cctx)

		mcid, err := api.PaychVoucherSubmit(ctx, ch, sv, nil, nil)
		if err != nil {
			return err
		}

		mwait, err := api.StateWaitMsg(ctx, mcid, build.MessageConfidence)
		if err != nil {
			return err
		}

		if mwait.Receipt.ExitCode != 0 {
			return fmt.Errorf("message execution failed (exit code %d)", mwait.Receipt.ExitCode)
		}

		_, _ = fmt.Fprintln(cctx.App.Writer, "channel updated successfully")

		return nil
	},
}

// 编码字符串
func EncodedString(sv *paych.SignedVoucher) (string, error) {
	buf := new(bytes.Buffer)
	if err := sv.MarshalCBOR(buf); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}
