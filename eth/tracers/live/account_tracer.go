package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers"
)

type TransactionDetail struct {
	TxHash    string
	Timestamp uint64
	From      string
	To        string
	Amount    *big.Int
	Token     string
}

type AccountTracer struct {
	accounts     map[string]bool
	transactions []TransactionDetail
	startedTxs   map[common.Hash]TransactionDetail
	txMux        sync.Mutex
	logBuffer    *bytes.Buffer
	logMux       sync.Mutex
	flushTicker  *time.Ticker
	ctx          context.Context
	cancelCtx    context.CancelFunc
	logFile      *os.File
	conn         net.Conn
}

func init() {
	tracers.LiveDirectory.Register("accountTracer", newAccountTracer)
}

func newAccountTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	var accounts []string
	if err := json.Unmarshal(cfg, &accounts); err != nil {
		return nil, err
	}

	tracer, err := NewAccountTracer(accounts)
	if err != nil {
		return nil, err
	}

	return &tracing.Hooks{
		OnTxStart:    tracer.OnTxStart,
		OnTxEnd:      tracer.OnTxEnd,
		OnBlockStart: tracer.OnBlockStart,
		OnBlockEnd:   tracer.OnBlockEnd,
		OnClose:      tracer.OnClose,
	}, nil
}

func NewAccountTracer(accounts []string) (*AccountTracer, error) {
	accountMap := make(map[string]bool)
	for _, account := range accounts {
		accountMap[account] = true
	}

	ctx, cancel := context.WithCancel(context.Background())

	logFile, err := os.OpenFile("account_tracer_output.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	conn, err := net.Dial("unix", "/tmp/account_tracer.sock")
	if err != nil {
		_ = logFile.Close()
		cancel()
		return nil, fmt.Errorf("failed to connect to socket: %w", err)
	}

	tracer := &AccountTracer{
		accounts:    accountMap,
		startedTxs:  make(map[common.Hash]TransactionDetail),
		logBuffer:   new(bytes.Buffer),
		flushTicker: time.NewTicker(1 * time.Minute), // Adjust the interval as needed
		ctx:         ctx,
		cancelCtx:   cancel,
		logFile:     logFile,
		conn:        conn,
	}

	go tracer.flushBufferPeriodically(ctx)
	go tracer.writeLogToFile(ctx)
	return tracer, nil
}

func (t *AccountTracer) flushBufferPeriodically(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.flushTicker.C:
			t.logMux.Lock()
			if t.logBuffer.Len() > 0 {
				_, err := t.logFile.Write(t.logBuffer.Bytes())
				if err != nil {
					log.Println("File write error:", err)
				}
				t.logBuffer.Reset()
			}
			t.logMux.Unlock()
		}
	}
}

func (t *AccountTracer) writeLogToFile(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Minute): // Adjust the interval as needed
			t.logMux.Lock()
			if t.logBuffer.Len() > 0 {
				_, err := t.logFile.Write(t.logBuffer.Bytes())
				if err != nil {
					log.Println("Log file write error:", err)
				}
				t.logBuffer.Reset()
			}
			t.logMux.Unlock()
		}
	}
}

func (t *AccountTracer) OnTxStart(vm *tracing.VMContext, tx *types.Transaction, from common.Address) {

	t.txMux.Lock()
	defer t.txMux.Unlock()

	logMessage := fmt.Sprintf("\n[AccountTracer.OnTxStart]: [from:%s][to:%s][txHash:%s]", from.Hex(), tx.To().Hex(), tx.Hash().Hex())
	fmt.Print(logMessage)

	t.logMux.Lock()
	t.logBuffer.WriteString(logMessage)
	t.logMux.Unlock()

	if t.accounts[from.Hex()] || t.accounts[tx.To().Hex()] {
		txDetail := TransactionDetail{
			TxHash:    tx.Hash().Hex(),
			Timestamp: vm.Time,
			From:      from.Hex(),
			To:        tx.To().Hex(),
			Amount:    tx.Value(),
			Token:     "ETH",
		}
		t.startedTxs[tx.Hash()] = txDetail
	}
}

func (t *AccountTracer) OnTxEnd(receipt *types.Receipt, err error) {
	t.logBuffer.WriteString("\n[AccountTracer.OnTxEnd]: Executing OnTxEnd hook\n")
	t.txMux.Lock()
	defer t.txMux.Unlock()

	logMessage := fmt.Sprintf("\n[AccountTracer.OnTxEnd]: [txHash:%s][status:%d]\n", receipt.TxHash.Hex(), receipt.Status)
	fmt.Print(logMessage)

	t.logMux.Lock()
	t.logBuffer.WriteString(logMessage)
	t.logMux.Unlock()

	marshal, err := json.Marshal(receipt)
	if err != nil {
		logMessage = fmt.Sprintf("\n[AccountTracer.OnTxEnd]: [failed marshalling error:%s]", err.Error())
	} else {
		logMessage = fmt.Sprintf("\n[AccountTracer.OnTxEnd]: [receipt:%s]", string(marshal))
	}
	fmt.Print(logMessage)

	t.logMux.Lock()
	t.logBuffer.WriteString(logMessage)
	t.logMux.Unlock()

	txHash := receipt.TxHash
	if txDetail, exists := t.startedTxs[txHash]; exists {
		if err == nil && receipt.Status == 1 {
			// Transaction was successful, track it
			t.transactions = append(t.transactions, txDetail)
		}
		// Remove the transaction from startedTxs
		delete(t.startedTxs, txHash)
	}
}

func (t *AccountTracer) OnBlockStart(event tracing.BlockEvent) {
	t.logBuffer.WriteString("\n[OnBlockStart]: Executing OnBlockStart hook\n")

	data, err := json.Marshal(event)
	if err != nil {
		log.Println("JSON marshal error:", err)
		return
	}

	t.logMux.Lock()
	t.logBuffer.Write(data)
	t.logBuffer.WriteByte('\n') // Add a newline or delimiter if needed
	t.logMux.Unlock()
}

func (t *AccountTracer) OnBlockEnd(err error) {
	t.logBuffer.WriteString("\n[OnBlockEnd]: Executing OnBlockEnd hook\n")
	// Handle block end event similarly
}

func (t *AccountTracer) OnClose() {
	t.logBuffer.WriteString("\n[OnClose]: Executing OnClose hook\n")
	t.flushTicker.Stop()
	t.cancelCtx()
	_ = t.conn.Close()
	_ = t.logFile.Close()
}

func (t *AccountTracer) GetResult() (json.RawMessage, error) {
	t.txMux.Lock()
	defer t.txMux.Unlock()

	return json.Marshal(t.transactions)
}
