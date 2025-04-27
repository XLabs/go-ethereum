package live

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/rlp"
	"log"
	"net"
	"os"
)

type xlabsTracerConfig struct {
	SocketFilePath string `json:"socketFilePath"` // Path to the unix-domain-socket
	LogFile        string `json:"path"`           // Path to the directory where the tracer logs will be stored
	MaxSize        int    `json:"maxSize"`        // MaxSize is the maximum size in megabytes of the tracer log file before it gets rotated. It defaults to 100 megabytes.
}

type xlabsTracer struct {
	conn              net.Conn
	logger            *log.Logger
	txReceipts        []*types.Receipt
	currentBlockEvent *tracing.BlockEvent
	ctx               context.Context
	cancelFunc        context.CancelFunc
	logFile           *os.File
}

type Event struct {
	Version        uint8
	LatestBlock    *types.Header
	FinalizedBlock *types.Header // Optional (nil if missing)
	SafeBlock      *types.Header // Optional (nil if missing)
	Receipts       []*types.Receipt
}

func init() {
	tracers.LiveDirectory.Register("xlabstracer", newXlabsTracer)
}

func newXlabsTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	var config xlabsTracerConfig
	if err := json.Unmarshal(cfg, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %v", err)
	}
	if config.LogFile == "" {
		return nil, errors.New("xlabstracer output path is required")
	}

	// Set up event logger
	logFile, err := os.OpenFile(config.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("opening geth.log: %v", err)
	}

	logger := log.New(logFile, "", log.LstdFlags)

	if config.SocketFilePath == "" {
		config.SocketFilePath = "/tmp/trace.sock"
		logger.Printf("socket file path: %s", config.SocketFilePath)
	}

	// Open the unix-domain-socket
	conn, err := net.Dial("unix", config.SocketFilePath)
	if err != nil {
		logger.Printf("failed to connect: %v", err)
		return nil, fmt.Errorf("xlabstracer init error: failed to connect to unix-domain-socket. error:%s", err.Error())
	}

	ctx, cancelFunc := context.WithCancel(context.Background())

	t := &xlabsTracer{
		conn:       conn,
		logger:     logger,
		logFile:    logFile,
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

func (s *xlabsTracer) onBlockStart(ev tracing.BlockEvent) {
	s.logger.Printf("block start: %v\n", ev)
	s.currentBlockEvent = &ev
}

func (t *xlabsTracer) onBlockEnd(err error) {
	defer t.cleanUp()

	if err != nil {
		return
	}

	payload := Event{
		Version:        1,
		LatestBlock:    t.currentBlockEvent.Block.Header(),
		FinalizedBlock: nil,
		SafeBlock:      nil,
		Receipts:       append([]*types.Receipt(nil), t.txReceipts...),
	}

	if t.currentBlockEvent.Finalized != nil {
		payload.FinalizedBlock = types.CopyHeader(t.currentBlockEvent.Finalized)
	}
	if t.currentBlockEvent.Safe != nil {
		payload.SafeBlock = types.CopyHeader(t.currentBlockEvent.Safe)
	}

	go t.sendUDSMessage(payload)
}

func (t *xlabsTracer) sendUDSMessage(payload Event) {
	var buf bytes.Buffer
	if err := rlp.Encode(&buf, payload); err != nil {
		t.logger.Printf("error encoding payload: %v", err)
		return
	}

	length := uint32(buf.Len())
	if err := binary.Write(t.conn, binary.BigEndian, length); err != nil {
		t.logger.Printf("error writing length prefix: %v", err)
		return
	}

	if _, err := t.conn.Write(buf.Bytes()); err != nil {
		t.logger.Printf("error writing payload: %v", err)
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
	s.logger.Printf("xlabstracer closing\n")
	s.cleanUp()
	s.cancelFunc()
	s.conn.Close()
	s.logFile.Close()
}
