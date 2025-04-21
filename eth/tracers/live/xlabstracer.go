package live

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/rlp"
	"gopkg.in/natefinch/lumberjack.v2"
	"net"
	"path/filepath"
)

func init() {
	tracers.LiveDirectory.Register("supply", newXlabsTracer)
}

func newXlabsTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	var config supplyTracerConfig
	if err := json.Unmarshal(cfg, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %v", err)
	}
	if config.Path == "" {
		return nil, errors.New("xlabstracer output path is required")
	}

	// Store traces in a rotating file
	logger := &lumberjack.Logger{
		Filename: filepath.Join(config.Path, "xlabstracer.jsonl"),
	}
	if config.MaxSize > 0 {
		logger.MaxSize = config.MaxSize
	}

	t := &xlabsTracer{}
	return &tracing.Hooks{
		OnBlockStart: t.onBlockStart,
		OnBlockEnd:   t.onBlockEnd,
		OnTxEnd:      t.OnTxEnd,
		//OnExit:           t.onExit,
		//OnClose:          t.onClose,
	}, nil
}

type xlabsTracer struct {
	txReceipts        []*types.Receipt
	currentBlockEvent *tracing.BlockEvent
	conn              net.Conn
}

type Event struct {
	LatestBlock    *types.Header
	FinalizedBlock *types.Header
	SafeBlock      *types.Header
	Receipts       []*types.Receipt
}

func (s *xlabsTracer) onBlockStart(ev tracing.BlockEvent) {
	s.currentBlockEvent = &ev
}

func (s *xlabsTracer) onBlockEnd(err error) {
	defer s.cleanUp()

	if err != nil {
		return
	}

	txRecps := make([]*types.Receipt, 0, len(s.txReceipts))
	copy(txRecps, s.txReceipts)

	newBlock := s.currentBlockEvent.Block.Header()
	finalizedBlock := types.CopyHeader(s.currentBlockEvent.Finalized)
	safeBlock := types.CopyHeader(s.currentBlockEvent.Safe)

	payload := Event{
		LatestBlock:    newBlock,
		FinalizedBlock: finalizedBlock,
		SafeBlock:      safeBlock,
		Receipts:       txRecps,
	}

	// Send the payload to the unix-domain-socket
	go s.sendUDSMessage(payload)
}

func (s *xlabsTracer) sendUDSMessage(payload Event) {

	var buf bytes.Buffer
	if err := rlp.Encode(&buf, payload); err != nil {
		fmt.Println(err)
		return
	}

	// prefix with 4-byte length to frame messages
	length := uint32(buf.Len())
	if err := binary.Write(s.conn, binary.BigEndian, length); err != nil {
		fmt.Println(err)
		return
	}

	_, err := conn.Write(buf.Bytes())
	if err != nil {
		fmt.Println(err)
	}
}

func (s *xlabsTracer) OnTxEnd(receipt *types.Receipt, err error) {
	if err == nil {
		s.txReceipts = append(s.txReceipts, receipt)
	}
}

func (s *xlabsTracer) cleanUp() {
	s.txReceipts = nil
	s.currentBlockEvent = nil
}
