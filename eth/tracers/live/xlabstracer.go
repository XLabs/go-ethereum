package live

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/rlp"
	"gopkg.in/natefinch/lumberjack.v2"
)

func init() {
	tracers.LiveDirectory.Register("supply", newXlabsTracer)
}

type xlabsTracerConfig struct {
	SocketFilePath string `json:"socketFilePath"` // Path to the unix-domain-socket
	LogFile        string `json:"path"`           // Path to the directory where the tracer logs will be stored
	MaxSize        int    `json:"maxSize"`        // MaxSize is the maximum size in megabytes of the tracer log file before it gets rotated. It defaults to 100 megabytes.
}

func newXlabsTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	var config xlabsTracerConfig
	if err := json.Unmarshal(cfg, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %v", err)
	}
	if config.LogFile == "" {
		return nil, errors.New("xlabstracer output path is required")
	}

	logger := &lumberjack.Logger{
		// Store traces in a rotating file
		Filename: filepath.Join(config.LogFile, "xlabstracer.jsonl"),
	}

	if config.MaxSize > 0 {
		logger.MaxSize = config.MaxSize
	}

	if config.SocketFilePath == "" {
		config.SocketFilePath = "/tmp/trace.sock"
		_, err := logger.Write([]byte(fmt.Sprintf("socket file path: %s", config.SocketFilePath)))
		if err != nil {
			return nil, fmt.Errorf("xlabstracer init error: failed to write socket file path to log. error:%s", err.Error())
		}
	}

	// Open the unix-domain-socket
	conn, err := net.Dial("unix", config.SocketFilePath)
	if err != nil {
		return nil, fmt.Errorf("xlabstracer init error: failed to connect to unix-domain-socket. error:%s", err.Error())
	}

	ctx, cancelFunc := context.WithCancel(context.Background())

	t := &xlabsTracer{
		conn:       conn,
		logger:     logger,
		ctx:        ctx,
		cancelFunc: cancelFunc,
	}

	return &tracing.Hooks{
		OnBlockStart: t.onBlockStart,
		OnBlockEnd:   t.onBlockEnd,
		OnTxEnd:      t.OnTxEnd,
		OnClose:      t.onClose,
	}, nil
}

type xlabsTracer struct {
	conn              net.Conn
	logger            *lumberjack.Logger
	txReceipts        []*types.Receipt
	currentBlockEvent *tracing.BlockEvent
	ctx               context.Context
	cancelFunc        context.CancelFunc
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

	// Copy all the data before moving on to the next block
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
		s.logger.Write([]byte(fmt.Sprintf("Error encoding payload: %v\n", err)))
		return
	}

	// prefix with 4-byte length to frame messages
	length := uint32(buf.Len())
	if err := binary.Write(s.conn, binary.BigEndian, length); err != nil {
		s.logger.Write([]byte(fmt.Sprintf("Error writing message length: %v\n", err)))
		return
	}

	_, err := s.conn.Write(buf.Bytes())
	if err != nil {
		s.logger.Write([]byte(fmt.Sprintf("Error writing message to socket: %v\n", err)))
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

func (s *xlabsTracer) onClose() {
	s.cleanUp()
	s.cancelFunc()
	s.conn.Close()
	s.logger.Close()
}
