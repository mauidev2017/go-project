package ethchain

import (
	"github.com/ethereum/eth-go/ethutil"
	"github.com/obscuren/secp256k1-go"
	"math/big"
)

var ContractAddr = []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

type Transaction struct {
	Nonce     uint64
	Recipient []byte
	Value     *big.Int
	Gas       *big.Int
	GasPrice  *big.Int
	Data      []byte
	Init      []byte
	v         byte
	r, s      []byte

	// Indicates whether this tx is a contract creation transaction
	contractCreation bool
}

func NewContractCreationTx(value, gasprice *big.Int, script []byte, init []byte) *Transaction {
	return &Transaction{Value: value, GasPrice: gasprice, Data: script, Init: init, contractCreation: true}
}

func NewTransactionMessage(to []byte, value, gasprice, gas *big.Int, data []byte) *Transaction {
	return &Transaction{Recipient: to, Value: value, GasPrice: gasprice, Gas: gas, Data: data}
}

func NewTransactionFromBytes(data []byte) *Transaction {
	tx := &Transaction{}
	tx.RlpDecode(data)

	return tx
}

func NewTransactionFromValue(val *ethutil.Value) *Transaction {
	tx := &Transaction{}
	tx.RlpValueDecode(val)

	return tx
}

func (tx *Transaction) Hash() []byte {
	data := []interface{}{tx.Nonce, tx.Value, tx.GasPrice, tx.Gas, tx.Recipient, string(tx.Data)}
	if tx.contractCreation {
		data = append(data, string(tx.Init))
	}

	return ethutil.Sha3Bin(ethutil.NewValue(data).Encode())
}

func (tx *Transaction) IsContract() bool {
	return tx.contractCreation
}

func (tx *Transaction) Signature(key []byte) []byte {
	hash := tx.Hash()

	sig, _ := secp256k1.Sign(hash, key)

	return sig
}

func (tx *Transaction) PublicKey() []byte {
	hash := tx.Hash()

	// If we don't make a copy we will overwrite the existing underlying array
	dst := make([]byte, len(tx.r))
	copy(dst, tx.r)

	sig := append(dst, tx.s...)
	sig = append(sig, tx.v-27)

	pubkey, _ := secp256k1.RecoverPubkey(hash, sig)

	return pubkey
}

func (tx *Transaction) Sender() []byte {
	pubkey := tx.PublicKey()

	// Validate the returned key.
	// Return nil if public key isn't in full format
	if pubkey[0] != 4 {
		return nil
	}

	return ethutil.Sha3Bin(pubkey[1:])[12:]
}

func (tx *Transaction) Sign(privk []byte) error {

	sig := tx.Signature(privk)

	tx.r = sig[:32]
	tx.s = sig[32:64]
	tx.v = sig[64] + 27

	return nil
}

// [ NONCE, VALUE, GASPRICE, GAS, TO, DATA, V, R, S ]
// [ NONCE, VALUE, GASPRICE, GAS, 0, CODE, INIT, V, R, S ]
func (tx *Transaction) RlpData() interface{} {
	data := []interface{}{tx.Nonce, tx.Value, tx.GasPrice, tx.Gas, tx.Recipient, tx.Data}

	if tx.contractCreation {
		data = append(data, tx.Init)
	}
	//d := ethutil.NewSliceValue(tx.Data).Slice()

	return append(data, tx.v, tx.r, tx.s)
}

func (tx *Transaction) RlpValue() *ethutil.Value {
	return ethutil.NewValue(tx.RlpData())
}

func (tx *Transaction) RlpEncode() []byte {
	return tx.RlpValue().Encode()
}

func (tx *Transaction) RlpDecode(data []byte) {
	tx.RlpValueDecode(ethutil.NewValueFromBytes(data))
}

func (tx *Transaction) RlpValueDecode(decoder *ethutil.Value) {
	tx.Nonce = decoder.Get(0).Uint()
	tx.Value = decoder.Get(1).BigInt()
	tx.GasPrice = decoder.Get(2).BigInt()
	tx.Gas = decoder.Get(3).BigInt()
	tx.Recipient = decoder.Get(4).Bytes()
	tx.Data = decoder.Get(5).Bytes()

	// If the list is of length 10 it's a contract creation tx
	if decoder.Len() == 10 {
		tx.contractCreation = true
		tx.Init = decoder.Get(6).Bytes()

		tx.v = byte(decoder.Get(7).Uint())
		tx.r = decoder.Get(8).Bytes()
		tx.s = decoder.Get(9).Bytes()
	} else {
		tx.v = byte(decoder.Get(6).Uint())
		tx.r = decoder.Get(7).Bytes()
		tx.s = decoder.Get(8).Bytes()
	}
}
