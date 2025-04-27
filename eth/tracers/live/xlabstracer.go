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
	Version        uint8         // Protocol version (start with 1)
	LatestBlock    *types.Header // Always required
	HasFinalized   bool          // If true, FinalizedBlock is present
	FinalizedBlock *types.Header // Optional
	HasSafe        bool          // If true, SafeBlock is present
	SafeBlock      *types.Header // Optional
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

func (s *xlabsTracer) onBlockEnd(err error) {
	defer s.cleanUp()

	if err != nil {
		return
	}

	// Copy all the data before moving on to the next block
	txRecps := make([]*types.Receipt, 0, len(s.txReceipts))
	copy(txRecps, s.txReceipts)

	newBlock := s.currentBlockEvent.Block.Header()
	var finalizedBlock *types.Header
	if s.currentBlockEvent.Finalized != nil {
		finalizedBlock = types.CopyHeader(s.currentBlockEvent.Finalized)
	} else {
		s.logger.Println("xlabsTracer: Finalized block is nil")
	}

	var safeBlock *types.Header
	if s.currentBlockEvent.Safe != nil {
		safeBlock = types.CopyHeader(s.currentBlockEvent.Safe)
	} else {
		s.logger.Println("xlabsTracer: Safe block is nil")
	}

	payload := Event{
		Version:        1,
		LatestBlock:    newBlock,
		HasFinalized:   s.currentBlockEvent.Finalized != nil,
		FinalizedBlock: finalizedBlock,
		HasSafe:        s.currentBlockEvent.Safe != nil,
		SafeBlock:      safeBlock,
		Receipts:       txRecps,
	}

	if s.currentBlockEvent.Finalized != nil {
		payload.FinalizedBlock = types.CopyHeader(s.currentBlockEvent.Finalized)
	}
	if s.currentBlockEvent.Safe != nil {
		payload.SafeBlock = types.CopyHeader(s.currentBlockEvent.Safe)
	}

	// Send the payload to the unix-domain-socket
	go s.sendUDSMessage(payload)
}

func (s *xlabsTracer) sendUDSMessage(payload Event) {
	var buf bytes.Buffer
	if err := rlp.Encode(&buf, payload); err != nil {
		s.logger.Printf("Error encoding payload: %v\n", err)
		return
	}

	// prefix with 4-byte length to frame messages
	length := uint32(buf.Len())
	if err := binary.Write(s.conn, binary.BigEndian, length); err != nil {
		s.logger.Printf("Error writing message length: %v\n", err)
		return
	}

	_, err := s.conn.Write(buf.Bytes())
	if err != nil {
		s.logger.Printf("Error writing message to socket: %v\n", err)
	}

	s.logger.Printf("Message sent successfully: blockNumber:%d\n", payload.LatestBlock.Number)
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
