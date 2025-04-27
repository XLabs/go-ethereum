package live

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	pb "github.com/ethereum/go-ethereum/eth/tracers/live/tracerproto"
)

func convertHeader(h *types.Header) *pb.Header {
	if h == nil {
		return nil
	}
	p := &pb.Header{
		ParentHash:    h.ParentHash.Bytes(),
		UncleHash:     h.UncleHash.Bytes(),
		Coinbase:      h.Coinbase.Bytes(),
		Root:          h.Root.Bytes(),
		TxHash:        h.TxHash.Bytes(),
		ReceiptHash:   h.ReceiptHash.Bytes(),
		Bloom:         h.Bloom.Bytes(),
		Difficulty:    h.Difficulty.Bytes(),
		Number:        h.Number.Bytes(),
		GasLimit:      h.GasLimit,
		GasUsed:       h.GasUsed,
		Time:          h.Time,
		Extra:         h.Extra,
		MixDigest:     h.MixDigest.Bytes(),
		Nonce:         h.Nonce[:], // [8]byte to []byte
		BlobGasUsed:   h.BlobGasUsed,
		ExcessBlobGas: h.ExcessBlobGas,
	}

	if h.BaseFee != nil {
		p.BaseFee = h.BaseFee.Bytes()
	}
	if h.WithdrawalsHash != nil {
		p.WithdrawalsHash = h.WithdrawalsHash.Bytes()
	}
	if h.ParentBeaconRoot != nil {
		p.ParentBeaconRoot = h.ParentBeaconRoot.Bytes()
	}

	return p
}

func convertReceipt(r *types.Receipt) *pb.Receipt {
	if r == nil {
		return nil
	}
	return &pb.Receipt{
		Status:            r.Status,
		CumulativeGasUsed: r.CumulativeGasUsed,
		Bloom:             r.Bloom.Bytes(),
		TxIndex:           uint64(r.TransactionIndex),
		GasUsed:           r.GasUsed,
		ContractAddress:   r.ContractAddress.Hex(),
		Logs:              convertLogs(r.Logs),
	}
}

func convertLogs(logs []*types.Log) []*pb.Log {
	result := make([]*pb.Log, 0, len(logs))
	for _, l := range logs {
		result = append(result, &pb.Log{
			Address:     l.Address.Hex(),
			Topics:      convertTopics(l.Topics),
			Data:        l.Data,
			BlockNumber: l.BlockNumber,
			TxHash:      l.TxHash.Hex(),
			TxIndex:     uint64(l.TxIndex),
			LogIndex:    uint64(l.Index),
			Removed:     l.Removed,
		})
	}
	return result
}

func convertTopics(topics []common.Hash) []string {
	result := make([]string, 0, len(topics))
	for _, t := range topics {
		result = append(result, t.Hex())
	}
	return result
}
