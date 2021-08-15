package miner

import (
	"context"
	"crypto/rand"
	"math"
	"time"

	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-bitfield"
	"github.com/filecoin-project/go-state-types/abi"

	proof2 "github.com/filecoin-project/specs-actors/v2/actors/runtime/proof"

	"github.com/filecoin-project/lotus/chain/types"
)

// 热身后赢得PoSt(时空证明)
func (m *Miner) winPoStWarmup(ctx context.Context) error {
	// 设置deadlines
	deadlines, err := m.api.StateMinerDeadlines(ctx, m.address, types.EmptyTSK)
	if err != nil {
		return xerrors.Errorf("getting deadlines: %w", err)
	}

	var sector abi.SectorNumber = math.MaxUint64

out:
	for dlIdx := range deadlines {
		// 放回dlIdx期限内的所有分区
		partitions, err := m.api.StateMinerPartitions(ctx, m.address, uint64(dlIdx), types.EmptyTSK)
		if err != nil {
			return xerrors.Errorf("getting partitions for deadline %d: %w", dlIdx, err)
		}

		for _, partition := range partitions {
			// 从分区中获取第一活跃扇区
			b, err := partition.ActiveSectors.First()
			if err == bitfield.ErrNoBitsSet {
				// 位域没有设置位
				continue
			}
			if err != nil {
				return err
			}

			// SectorNumber 是扇区的数字标识符
			sector = abi.SectorNumber(b)
			break out
		}
	}

	if sector == math.MaxUint64 {
		// 跳过获胜的 PoSt 预热，没有扇区
		log.Info("skipping winning PoSt warmup, no sectors")
		return nil
	}

	log.Infow("starting winning PoSt warmup", "sector", sector)
	start := time.Now()

	// RandomnessLength 是随机切片的长度。
	var r abi.PoStRandomness = make([]byte, abi.RandomnessLength)
	_, _ = rand.Read(r)

	// StateSectorGetInfo 返回指定矿工扇区的链上信息。如果找不到扇区信息，则返回 null
	si, err := m.api.StateSectorGetInfo(ctx, m.address, sector, types.EmptyTSK)
	if err != nil {
		return xerrors.Errorf("getting sector info: %w", err)
	}

	// 计算证明
	_, err = m.epp.ComputeProof(ctx, []proof2.SectorInfo{
		{
			SealProof:    si.SealProof,
			SectorNumber: sector,
			SealedCID:    si.SealedCID,
		},
	}, r)
	if err != nil {
		return xerrors.Errorf("failed to compute proof: %w", err)
	}

	log.Infow("winning PoSt warmup successful", "took", time.Now().Sub(start))
	return nil
}

func (m *Miner) doWinPoStWarmup(ctx context.Context) {
	err := m.winPoStWarmup(ctx)
	if err != nil {
		log.Errorw("winning PoSt warmup failed", "error", err)
	}
}
