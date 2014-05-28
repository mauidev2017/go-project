package ethchain

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"github.com/ethereum/eth-go/ethutil"
	"github.com/ethereum/eth-go/ethwire"
	"log"
	"math/big"
	"sync"
)

const (
	txPoolQueueSize = 50
)

type TxPoolHook chan *Transaction
type TxMsgTy byte

const (
	TxPre = iota
	TxPost
)

type TxMsg struct {
	Tx   *Transaction
	Type TxMsgTy
}

func FindTx(pool *list.List, finder func(*Transaction, *list.Element) bool) *Transaction {
	for e := pool.Front(); e != nil; e = e.Next() {
		if tx, ok := e.Value.(*Transaction); ok {
			if finder(tx, e) {
				return tx
			}
		}
	}

	return nil
}

type TxProcessor interface {
	ProcessTransaction(tx *Transaction)
}

// The tx pool a thread safe transaction pool handler. In order to
// guarantee a non blocking pool we use a queue channel which can be
// independently read without needing access to the actual pool. If the
// pool is being drained or synced for whatever reason the transactions
// will simple queue up and handled when the mutex is freed.
type TxPool struct {
	Ethereum EthManager
	// The mutex for accessing the Tx pool.
	mutex sync.Mutex
	// Queueing channel for reading and writing incoming
	// transactions to
	queueChan chan *Transaction
	// Quiting channel
	quit chan bool
	// The actual pool
	pool *list.List

	SecondaryProcessor TxProcessor

	subscribers []chan TxMsg
}

func NewTxPool(ethereum EthManager) *TxPool {
	return &TxPool{
		//server:    s,
		mutex:     sync.Mutex{},
		pool:      list.New(),
		queueChan: make(chan *Transaction, txPoolQueueSize),
		quit:      make(chan bool),
		Ethereum:  ethereum,
	}
}

// Blocking function. Don't use directly. Use QueueTransaction instead
func (pool *TxPool) addTransaction(tx *Transaction) {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()

	pool.pool.PushBack(tx)

	// Broadcast the transaction to the rest of the peers
	pool.Ethereum.Broadcast(ethwire.MsgTxTy, []interface{}{tx.RlpData()})
}

// Process transaction validates the Tx and processes funds from the
// sender to the recipient.
func (pool *TxPool) ProcessTransaction(tx *Transaction, state *State, toContract bool) (gas *big.Int, err error) {
	defer func() {
		if r := recover(); r != nil {
			ethutil.Config.Log.Infoln(r)
			err = fmt.Errorf("%v", r)
		}
	}()

	gas = new(big.Int)
	addGas := func(g *big.Int) { gas.Add(gas, g) }

	// Get the sender
	sender := state.GetAccount(tx.Sender())

	if sender.Nonce != tx.Nonce {
		err = fmt.Errorf("[TXPL] Invalid account nonce, state nonce is %d transaction nonce is %d instead", sender.Nonce, tx.Nonce)
		return
	}

	txTotalBytes := big.NewInt(int64(len(tx.Data)))
	txTotalBytes.Div(txTotalBytes, ethutil.Big32)
	addGas(new(big.Int).Mul(txTotalBytes, GasSStore))

	// Make sure there's enough in the sender's account. Having insufficient
	// funds won't invalidate this transaction but simple ignores it.
	//totAmount := new(big.Int).Add(tx.Value, new(big.Int).Mul(TxFee, TxFeeRat))
	totAmount := new(big.Int).Add(tx.Value, new(big.Int).Mul(tx.Gas, tx.GasPrice))
	if sender.Amount.Cmp(totAmount) < 0 {
		err = fmt.Errorf("[TXPL] Insufficient amount in sender's (%x) account", tx.Sender())
		return
	}

	// Get the receiver
	receiver := state.GetAccount(tx.Recipient)
	sender.Nonce += 1

	// Send Tx to self
	if bytes.Compare(tx.Recipient, tx.Sender()) == 0 {
		addGas(GasTx)
		// Subtract the fee
		sender.SubAmount(new(big.Int).Mul(GasTx, tx.GasPrice))
	} else {
		// Subtract the amount from the senders account
		sender.SubAmount(totAmount)

		// Add the amount to receivers account which should conclude this transaction
		receiver.AddAmount(tx.Value)

		state.UpdateStateObject(receiver)
	}

	state.UpdateStateObject(sender)

	ethutil.Config.Log.Infof("[TXPL] Processed Tx %x\n", tx.Hash())

	// Notify all subscribers
	pool.Ethereum.Reactor().Post("newTx:post", tx)

	return
}

func (pool *TxPool) ValidateTransaction(tx *Transaction) error {
	// Get the last block so we can retrieve the sender and receiver from
	// the merkle trie
	block := pool.Ethereum.BlockChain().CurrentBlock
	// Something has gone horribly wrong if this happens
	if block == nil {
		return errors.New("[TXPL] No last block on the block chain")
	}

	// Get the sender
	//sender := pool.Ethereum.StateManager().procState.GetAccount(tx.Sender())
	sender := pool.Ethereum.StateManager().CurrentState().GetAccount(tx.Sender())

	totAmount := new(big.Int).Add(tx.Value, new(big.Int).Mul(TxFee, TxFeeRat))
	// Make sure there's enough in the sender's account. Having insufficient
	// funds won't invalidate this transaction but simple ignores it.
	if sender.Amount.Cmp(totAmount) < 0 {
		return fmt.Errorf("[TXPL] Insufficient amount in sender's (%x) account", tx.Sender())
	}

	// Increment the nonce making each tx valid only once to prevent replay
	// attacks

	return nil
}

func (pool *TxPool) queueHandler() {
out:
	for {
		select {
		case tx := <-pool.queueChan:
			hash := tx.Hash()
			foundTx := FindTx(pool.pool, func(tx *Transaction, e *list.Element) bool {
				return bytes.Compare(tx.Hash(), hash) == 0
			})

			if foundTx != nil {
				break
			}

			// Validate the transaction
			err := pool.ValidateTransaction(tx)
			if err != nil {
				ethutil.Config.Log.Debugln("Validating Tx failed", err)
			} else {
				// Call blocking version.
				pool.addTransaction(tx)

				// Notify the subscribers
				pool.Ethereum.Reactor().Post("newTx:pre", tx)
			}
		case <-pool.quit:
			break out
		}
	}
}

func (pool *TxPool) QueueTransaction(tx *Transaction) {
	pool.queueChan <- tx
}

func (pool *TxPool) CurrentTransactions() []*Transaction {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()

	txList := make([]*Transaction, pool.pool.Len())
	i := 0
	for e := pool.pool.Front(); e != nil; e = e.Next() {
		tx := e.Value.(*Transaction)

		txList[i] = tx

		i++
	}

	return txList
}

func (pool *TxPool) RemoveInvalid(state *State) {
	for e := pool.pool.Front(); e != nil; e = e.Next() {
		tx := e.Value.(*Transaction)
		sender := state.GetAccount(tx.Sender())
		err := pool.ValidateTransaction(tx)
		if err != nil || sender.Nonce != tx.Nonce {
			pool.pool.Remove(e)
		}
	}
}

func (pool *TxPool) Flush() []*Transaction {
	txList := pool.CurrentTransactions()

	// Recreate a new list all together
	// XXX Is this the fastest way?
	pool.pool = list.New()

	return txList
}

func (pool *TxPool) Start() {
	go pool.queueHandler()
}

func (pool *TxPool) Stop() {
	close(pool.quit)

	pool.Flush()

	log.Println("[TXP] Stopped")
}
