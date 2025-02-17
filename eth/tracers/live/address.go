package live

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
)

var balanceOfAddrSignature = crypto.Keccak256Hash([]byte("balanceOf(address)")).Hex()

func init() {
	tracers.LiveDirectory.Register("address", newAddressTracer)
}

// addressTracer is a live tracer that tracks all transactions that an address was involved in.
type addressTracer struct {
	addresses []common.Address
	writer    map[common.Address]*csv.Writer

	currentHash   common.Hash
	currentSender common.Address
}

type addressTracerConfig struct {
	Path      string   `json:"path"`      // Path to the directory where the tracer logs will be stored
	Addresses []string `json:"addresses"` // Addresses to be watched
}

func newAddressTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	var config addressTracerConfig
	if cfg != nil {
		if err := json.Unmarshal(cfg, &config); err != nil {
			return nil, fmt.Errorf("failed to parse config: %v", err)
		}
		if config.Path == "" {
			return nil, fmt.Errorf("no path provided")
		}
	}

	var (
		addresses []common.Address
		writer    = make(map[common.Address]*csv.Writer)
	)
	for _, addr := range config.Addresses {
		a := common.HexToAddress(addr)
		addresses = append(addresses, a)
		file, err := os.Create(fmt.Sprintf("%v/%v.csv", config.Path, addr))
		if err != nil {
			return nil, err
		}
		writer[a] = csv.NewWriter(file)
		writer[a].Write([]string{
			"TxHash",
			"Sender",
			"Currency",
			"Previous",
			"New",
			"Reason",
		})
		writer[a].Flush()
	}

	t := &addressTracer{
		addresses: addresses,
		writer:    writer,
	}
	return &tracing.Hooks{
		OnTxStart:       t.OnTxStart,
		OnBalanceChange: t.OnBalanceChange,
		OnStorageChange: t.OnStorageChange,
		OnLog:           t.OnLog,
	}, nil
}

func (t *addressTracer) OnTxStart(vm *tracing.VMContext, tx *types.Transaction, from common.Address) {
	t.currentHash = tx.Hash()
	t.currentSender = from
}

func (t *addressTracer) OnBalanceChange(a common.Address, prev, new *big.Int, reason tracing.BalanceChangeReason) {
	for _, addr := range t.addresses {
		if addr == a {
			t.writeRecord(addr, "ETH", prev.String(), new.String(), fmt.Sprint(reason))
			break
		}
	}
}

var (
	transferTopic   = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
	transferType, _ = abi.NewType("tuple(address,address,uint256)", "", nil)
)

func (t *addressTracer) OnLog(l *types.Log) {
	for _, topic := range l.Topics {
		if topic == transferTopic {
			//unpacked, err := (abi.Arguments).Unpack(l.Data[4:]) TODO: asi esta en el articulo
			args := abi.Arguments{}
			unpacked, err := args.Unpack(l.Data[4:])
			if err != nil {
				continue
			}
			from, ok := unpacked[0].(common.Address)
			if !ok {
				continue
			}
			to, ok := unpacked[1].(common.Address)
			if !ok {
				continue
			}
			tokens, ok := unpacked[2].(*big.Int)
			if !ok {
				continue
			}
			for _, addr := range t.addresses {
				if addr == from || addr == to {
					t.writeRecord(addr, l.Address.Hex(), "", tokens.String(), "token transfer")
					break
				}
			}
		}
	}
}

func (t *addressTracer) OnStorageChange(a common.Address, k, prev, new common.Hash) {
	slot := common.BytesToAddress(k.Bytes()[12:])
	for _, addr := range t.addresses {
		if addr == slot {
			t.writeRecord(addr, a.Hex(), prev.String(), new.String(), "token transfer")
			break
		}
	}
}

func isERC20Token(addr common.Address, state tracing.StateDB) bool {
	// Step 1: Check if contract exists at address
	code := state.GetCode(addr)
	if len(code) == 0 {
		return false // No code = Externally Owned Account (EOA), not a contract
	}

	// Step 2: Check if contract implements `balanceOf(address)`
	balanceOfSignature := balanceOfAddrSignature[:10] // First 4 bytes of function selector
	if bytes.Contains(code, []byte(balanceOfSignature)) {
		return true
	}

	return false
}

// isERC20BalanceSlot checks if a storage slot corresponds to an ERC-20 balance mapping
func isERC20BalanceSlot(addr common.Address, slot common.Hash) bool {
	expectedSlot := crypto.Keccak256Hash(append(addr.Bytes(), common.LeftPadBytes(big.NewInt(0).Bytes(), 32)...))
	return slot == expectedSlot
}

func (t *addressTracer) writeRecord(addr common.Address, currency, previous, new, reason string) {
	t.writer[addr].Write([]string{
		t.currentHash.Hex(),
		t.currentSender.Hex(),
		currency,
		previous,
		new,
		reason,
	})
	t.writer[addr].Flush()
}
