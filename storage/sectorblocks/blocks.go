package sectorblocks

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/namespace"
	"github.com/ipfs/go-datastore/query"
	dshelp "github.com/ipfs/go-ipfs-ds-help"
	"golang.org/x/xerrors"

	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/specs-storage/storage"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/node/modules/dtypes"
)

// 密封序列化
type SealSerialization uint8

const (
	SerializationUnixfs0 SealSerialization = 'u'
)

var dsPrefix = datastore.NewKey("/sealedblocks")

var ErrNotFound = errors.New("not found")

// 交易ID转DsKey
func DealIDToDsKey(dealID abi.DealID) datastore.Key {
	buf := make([]byte, binary.MaxVarintLen64)
	size := binary.PutUvarint(buf, uint64(dealID))
	return dshelp.NewKeyFromBinary(buf[:size])
}

// DsKey转交易ID
func DsKeyToDealID(key datastore.Key) (uint64, error) {
	buf, err := dshelp.BinaryFromDsKey(key)
	if err != nil {
		return 0, err
	}
	dealID, _ := binary.Uvarint(buf)
	return dealID, nil
}

// 扇区构建器，定义了接口
type SectorBuilder interface {
	SectorAddPieceToAny(ctx context.Context, size abi.UnpaddedPieceSize, r storage.Data, d api.PieceDealInfo) (api.SectorOffset, error)
	SectorsStatus(ctx context.Context, sid abi.SectorNumber, showOnChainInfo bool) (api.SectorInfo, error)
}

// 扇区块
type SectorBlocks struct {
	SectorBuilder

	keys  datastore.Batching
	keyLk sync.Mutex
}

// 新扇区块
func NewSectorBlocks(sb SectorBuilder, ds dtypes.MetadataDS) *SectorBlocks {
	sbc := &SectorBlocks{
		SectorBuilder: sb,
		keys:          namespace.Wrap(ds, dsPrefix),
	}

	return sbc
}

func (st *SectorBlocks) writeRef(dealID abi.DealID, sectorID abi.SectorNumber, offset abi.PaddedPieceSize, size abi.UnpaddedPieceSize) error {
	st.keyLk.Lock() // TODO: make this multithreaded
	defer st.keyLk.Unlock()

	v, err := st.keys.Get(DealIDToDsKey(dealID))
	if err == datastore.ErrNotFound {
		err = nil
	}
	if err != nil {
		return xerrors.Errorf("getting existing refs: %w", err)
	}

	var refs api.SealedRefs
	if len(v) > 0 {
		if err := cborutil.ReadCborRPC(bytes.NewReader(v), &refs); err != nil {
			return xerrors.Errorf("decoding existing refs: %w", err)
		}
	}

	refs.Refs = append(refs.Refs, api.SealedRef{
		SectorID: sectorID,
		Offset:   offset,
		Size:     size,
	})

	newRef, err := cborutil.Dump(&refs)
	if err != nil {
		return xerrors.Errorf("serializing refs: %w", err)
	}
	return st.keys.Put(DealIDToDsKey(dealID), newRef) // TODO: batch somehow
}

func (st *SectorBlocks) AddPiece(ctx context.Context, size abi.UnpaddedPieceSize, r io.Reader, d api.PieceDealInfo) (abi.SectorNumber, abi.PaddedPieceSize, error) {
	so, err := st.SectorBuilder.SectorAddPieceToAny(ctx, size, r, d)
	if err != nil {
		return 0, 0, err
	}

	// TODO: DealID has very low finality here
	err = st.writeRef(d.DealID, so.Sector, so.Offset, size)
	if err != nil {
		return 0, 0, xerrors.Errorf("writeRef: %w", err)
	}

	return so.Sector, so.Offset, nil
}

func (st *SectorBlocks) List() (map[uint64][]api.SealedRef, error) {
	res, err := st.keys.Query(query.Query{})
	if err != nil {
		return nil, err
	}

	ents, err := res.Rest()
	if err != nil {
		return nil, err
	}

	out := map[uint64][]api.SealedRef{}
	for _, ent := range ents {
		dealID, err := DsKeyToDealID(datastore.RawKey(ent.Key))
		if err != nil {
			return nil, err
		}

		var refs api.SealedRefs
		if err := cborutil.ReadCborRPC(bytes.NewReader(ent.Value), &refs); err != nil {
			return nil, err
		}

		out[dealID] = refs.Refs
	}

	return out, nil
}

func (st *SectorBlocks) GetRefs(dealID abi.DealID) ([]api.SealedRef, error) { // TODO: track local sectors
	ent, err := st.keys.Get(DealIDToDsKey(dealID))
	if err == datastore.ErrNotFound {
		err = ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var refs api.SealedRefs
	if err := cborutil.ReadCborRPC(bytes.NewReader(ent), &refs); err != nil {
		return nil, err
	}

	return refs.Refs, nil
}

func (st *SectorBlocks) GetSize(dealID abi.DealID) (uint64, error) {
	refs, err := st.GetRefs(dealID)
	if err != nil {
		return 0, err
	}

	return uint64(refs[0].Size), nil
}

func (st *SectorBlocks) Has(dealID abi.DealID) (bool, error) {
	// TODO: ensure sector is still there
	return st.keys.Has(DealIDToDsKey(dealID))
}
