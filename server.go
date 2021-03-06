package main

import (
	"bytes"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
)

const protocol = "tcp"
const nodeVersion = 1
const commandLength = 12

var nodeAddress string
var miningAddress string

// knownNdoes[0] is a full node which just routes, transfers transactions, verifies received block and keep full copy of blockchain
var fullNodes = []string{"localhost:3000"}

// nodeAddress is not included in knownNodes.
var knownNodes = make(map[string]int)

// transactions that need to be transferred
var blocksInTransit = make(map[string]int)

// store pending transactions
var mempool = make(map[string]Transaction)

type addr struct {
	AddrList []string
}

type blockC struct {
	Block    []byte
	AddrFrom string
}

type getblocks struct {
	AddrFrom string
}

type getdata struct {
	AddrFrom string
	Type     string
	ID       []byte
}

// inventory
type inv struct {
	AddrFrom string
	Type     string
	Items    [][]byte
}

type tx struct {
	AddFrom     string
	Transaction []byte
}

type version struct {
	Version    int
	BestHeight int
	AddrFrom   string
}

func commandToBytes(command string) []byte {
	var bytes [commandLength]byte

	for i, c := range command {
		bytes[i] = byte(c)
	}

	return bytes[:]
}

func bytesToCommand(bytes []byte) string {
	var command []byte

	for _, b := range bytes {
		// The length of commands is not fixed, which always smaller than commandLength.
		if b != 0x00 {
			command = append(command, b)
		}
	}

	return fmt.Sprintf("%s", command)
}

func extractCommand(request []byte) []byte {
	return request[:commandLength]
}

func requestBlocks() {
	for node, _ := range knownNodes {
		sendGetBlocks(node)
	}
}

func addAddress2KnownNodes(addrs []string) {
	knownNodes = AddSlice2StrMap(knownNodes, addrs)
	delete(knownNodes, nodeAddress)
}

func addAddress2BlockInTransit(addrs [][]byte) {
	for _, bbid := range addrs {
		blocksInTransit[hex.EncodeToString(bbid)]++
	}

	delete(blocksInTransit, nodeAddress)
}

func sendAddr(address string) {

	nodes := addr{StrMap2Slice(knownNodes)}
	nodes.AddrList = append(nodes.AddrList, nodeAddress)
	payload := gobEncode(nodes)
	request := append(commandToBytes("addr"), payload...)

	sendData(address, request)
}

func sendBlock(address string, b *Block) {
	data := blockC{b.Serialize(), nodeAddress}
	payload := gobEncode(data)
	request := append(commandToBytes("block"), payload...)

	sendData(address, request)
}

func sendData(addr string, data []byte) {
	conn, err := net.Dial(protocol, addr)
	if err != nil {
		fmt.Printf("%s is not available\n", addr)
		// delete invalid address
		delete(knownNodes, addr)
		return
	}
	defer conn.Close()

	_, err = io.Copy(conn, bytes.NewReader(data))
	if err != nil {
		log.Panic(err)
	}
}

func sendInv(address, kind string, items [][]byte) {
	inventory := inv{nodeAddress, kind, items}
	payload := gobEncode(inventory)
	request := append(commandToBytes("inv"), payload...)

	sendData(address, request)
}

func sendGetBlocks(address string) {
	payload := gobEncode(getblocks{nodeAddress})
	request := append(commandToBytes("getblocks"), payload...)

	sendData(address, request)
}

func sendGetData(address, kind string, id []byte) {
	payload := gobEncode(getdata{nodeAddress, kind, id})
	request := append(commandToBytes("getdata"), payload...)

	sendData(address, request)
}

func sendTx(addr string, tnx *Transaction) {
	data := tx{nodeAddress, tnx.Serialize()}
	payload := gobEncode(data)
	request := append(commandToBytes("tx"), payload...)

	sendData(addr, request)
}

func sendVersion(addr string, bc *Blockchain) {
	bestHeight, _ := bc.GetBestHeight()
	payload := gobEncode(version{nodeVersion, bestHeight, nodeAddress})

	request := append(commandToBytes("version"), payload...)

	sendData(addr, request)
}

func handleAddr(request []byte) {
	var buff bytes.Buffer
	var payload addr

	buff.Write(request[commandLength:])
	dec := gob.NewDecoder(&buff)
	err := dec.Decode(&payload)
	if err != nil {
		log.Panic(err)
	}
	// Add new address to knownNodes
	addAddress2KnownNodes(payload.AddrList)
	fmt.Printf("There are %d known nodes now!\n", len(knownNodes))
	requestBlocks()
}

func handleBlock(request []byte, bc *Blockchain) {
	var buff bytes.Buffer
	var payload blockC

	buff.Write(request[commandLength:])
	dec := gob.NewDecoder(&buff)
	err := dec.Decode(&payload)
	if err != nil {
		log.Panic(err)
	}

	blockData := payload.Block
	/*
		Serialize()和DeserializeBlock()这对好像有点问题
	*/
	block := DeserializeBlock(blockData)

	nodeID := nodeAddress[len(nodeAddress)-4 : len(nodeAddress)]

	if block != nil {
		/*
			The received block is different from the one in database (it cannot pass the pow check).
			You can check the merkle root.
			I cannnot figure out. So I disable pow check in isGenesisBlock function first.

		*/
		if isGenesisBlock(block) {
			fmt.Println("Receive a genesis block.")
			if !dbExists(fmt.Sprintf(dbFile, nodeID)) {
				bc = CopyGenesisBlock(nodeID, block)
				fmt.Printf("Accept that genesis block %x and create a blockchain", block.Hash)
				utxo := UTXOSet{bc}
				utxo.Reindex()

			}
		} else if dbExists(fmt.Sprintf(dbFile, nodeID)) {
			utxo := UTXOSet{bc}
			if utxo.VerifyBlock(block, true) {
				fmt.Println("Receive a block.")
				bc.AddBlock(block)
				utxo.Update(block)
				fmt.Printf("Added block %x\n", block.Hash)

			}
		}
	}

	if len(blocksInTransit) > 0 {
		var blockHash string

		for blockHash, _ = range blocksInTransit {
			// get just one block
			target, _ := hex.DecodeString(blockHash)
			sendGetData(payload.AddrFrom, "block", target)
			break
		}

		// renew blocksInTransit
		delete(blocksInTransit, blockHash)
	}
}

func handleInv(request []byte, bc *Blockchain) {
	var buff bytes.Buffer
	var payload inv

	buff.Write(request[commandLength:])
	dec := gob.NewDecoder(&buff)
	err := dec.Decode(&payload)
	if err != nil {
		log.Panic(err)
	}

	fmt.Printf("%s received inventory from %s with %d %s\n", nodeAddress, payload.AddrFrom, len(payload.Items), payload.Type)

	// payload contains AddrFrom, Type and slice of id
	if payload.Type == "block" {
		// blocksInTransit variable to track block should be downloaded
		addAddress2BlockInTransit(payload.Items)

		// Ask for one id in the payload
		var blockHash string
		for blockHash, _ = range blocksInTransit {
			target, _ := hex.DecodeString(blockHash)
			sendGetData(payload.AddrFrom, "block", target)
			fmt.Printf("Then I ask %s for block: %s\n", payload.AddrFrom, blockHash)
			break
		}

		// update
		delete(blocksInTransit, blockHash)
	}

	if payload.Type == "tx" {
		// Ask for one id in the payload
		for _, txID := range payload.Items {
			if mempool[hex.EncodeToString(txID)].ID == nil {
				sendGetData(payload.AddrFrom, "tx", txID)
				break
			}
		}

	}
}

func handleGetBlocks(request []byte, bc *Blockchain) {
	var buff bytes.Buffer
	var payload getblocks

	buff.Write(request[commandLength:])
	dec := gob.NewDecoder(&buff)
	err := dec.Decode(&payload)
	if err != nil {
		log.Panic(err)
	}

	blocks := bc.GetBlockHashes()
	sendInv(payload.AddrFrom, "block", blocks)
}

// getdata is a request for certain block or transaction, and it can contain only one block/transaction ID.
func handleGetData(request []byte, bc *Blockchain) {
	var buff bytes.Buffer
	var payload getdata

	buff.Write(request[commandLength:])
	dec := gob.NewDecoder(&buff)
	err := dec.Decode(&payload)
	if err != nil {
		log.Panic(err)
	}

	if payload.Type == "block" {

		fmt.Printf("I receive a request for block: %s\n", hex.EncodeToString(payload.ID))

		block, err := bc.GetBlock(payload.ID)

		fmt.Printf("Prev. block: %x\n", block.PrevBlockHash)
		fmt.Printf("Merkle Root: %x\n", block.HashTransactions())
		fmt.Printf("Timestamp: %x\n", IntToHex(block.Timestamp))
		fmt.Printf("Nonce: %x\n", IntToHex(int64(block.Nonce)))

		if err != nil {
			fmt.Printf("I don't have block: %s\n", hex.EncodeToString(payload.ID))
			return
		}

		fmt.Printf("I have block: %s and send it to %s\n", hex.EncodeToString(payload.ID), payload.AddrFrom)
		sendBlock(payload.AddrFrom, &block)
	}

	if payload.Type == "tx" {
		txID := hex.EncodeToString(payload.ID)
		tx := mempool[txID]

		sendTx(payload.AddrFrom, &tx)
	}
}

//
func handleTx(reuqest []byte, bc *Blockchain) {
	var buff bytes.Buffer
	var payload tx

	buff.Write(reuqest[commandLength:])
	dec := gob.NewDecoder(&buff)
	err := dec.Decode(&payload)
	if err != nil {
		log.Panic(err)
	}

	utxo := UTXOSet{bc}

	txData := payload.Transaction
	newTx := DeserializeTransaction(txData)
	// no coinbase transacton in the mempool
	if utxo.VerifyTransaction(&newTx) && !newTx.IsCoinbase() {
		// use map can sure that transactions in the block are different
		mempool[hex.EncodeToString(newTx.ID)] = newTx
	}

	/*
		Checks whether the current node is the central one.
		In our implementation, the central node won’t mine blocks.
		Instead, it’ll forward the new transactions to other nodes in the network.
		即 转发功能
	*/
	if ElementInStrSlice(fullNodes, nodeAddress) {
		for node, _ := range knownNodes {
			if node != nodeAddress && node != payload.AddFrom {
				sendInv(node, "tx", [][]byte{newTx.ID})
			}
		}
	} else { // Only for miner nodes
		if len(mempool) >= 2 && len(miningAddress) > 0 {
		MineTransactions:
			var txs []*Transaction

			for id := range mempool {
				tx := mempool[id]
				if utxo.VerifyTransaction(&tx) {
					txs = append(txs, &tx)
				}
			}

			var tmpTxs []*Transaction
			count := make(map[string]map[int]int)
			// prevent two pending transactions use the same utxo

			for _, tx := range txs {
				// pay attention to coinbase
				ok := true
				for _, vin := range tx.Vin {
					count[hex.EncodeToString(vin.Txid)][vin.Vout]++
					if count[hex.EncodeToString(vin.Txid)][vin.Vout] != 1 {
						ok = false
						delete(mempool, hex.EncodeToString(tx.ID))
						break
					}
				}
				if ok {
					tmpTxs = append(tmpTxs, tx)
				}
			}

			txs = tmpTxs

			if len(txs) == 0 {
				fmt.Println("All transactions are invalid! Waiting for new ones...")
				return
			}

			cbTx := NewCoinbaseTX(miningAddress, "")
			txs = append(txs, cbTx)

			newBlock := bc.MineBlock(txs, &utxo)
			utxo.Update(newBlock)

			fmt.Println("New block is mined!")

			for _, tx := range txs {
				txID := hex.EncodeToString(tx.ID)
				delete(mempool, txID)
			}

			for node, _ := range knownNodes {
				if node != nodeAddress {
					sendInv(node, "block", [][]byte{newBlock.Hash})
				}
			}

			if len(mempool) > 0 {
				goto MineTransactions
			}
		}
	}
}

func handleVersion(request []byte, bc *Blockchain) {
	var buff bytes.Buffer
	var payload version

	buff.Write(request[commandLength:])
	dec := gob.NewDecoder(&buff)
	err := dec.Decode(&payload)
	if err != nil {
		log.Panic(err)
	}

	/*//////////magic codes///////////////////////
	// Without that magic codes, the block ready to send is not equal to
	// the one of database (it cannot pass the pow check).
	// I cannot figure out. I Guess it is relate to
	// the concurrency of database

	if bc != nil {
		fmt.Println("Handle GetBlocks")
		block, _ := bc.GetBlock(bc.tip)
		fmt.Printf("Tip Merkle Root: %x\n", block.HashTransactions())
	}
	*/ ///////////////////////////////////////////

	myBestHeight, _ := bc.GetBestHeight()

	foreignerBestHeight := payload.BestHeight
	fmt.Printf("%s receives version from %s\n", nodeAddress, payload.AddrFrom)
	if myBestHeight < foreignerBestHeight {
		fmt.Printf("%s has older version of blockchain. So ask %s for newer version.\n", nodeAddress, payload.AddrFrom)
		sendGetBlocks(payload.AddrFrom)
	} else if myBestHeight > foreignerBestHeight {
		fmt.Printf("%s has newer version of blockchain. So inform %s with newer version.\n", nodeAddress, payload.AddrFrom)
		sendVersion(payload.AddrFrom, bc)
	}

	knownNodes = AddSlice2StrMap(knownNodes, []string{payload.AddrFrom})
}

func handleConnection(conn net.Conn, bc *Blockchain) {

	request, err := ioutil.ReadAll(conn)
	if err != nil {
		log.Panic(err)
	}

	command := bytesToCommand(request[:commandLength])
	fmt.Printf("Received %s command\n", command)

	switch command {
	case "addr":
		handleAddr(request)

	case "block":
		handleBlock(request, bc)

	case "inv":
		handleInv(request, bc)

	case "getblocks":
		handleGetBlocks(request, bc)

	case "getdata":
		handleGetData(request, bc)

	case "tx":
		handleTx(request, bc)

	case "version":
		handleVersion(request, bc)

	default:
		fmt.Println("Unknown command!")
	}

	if bc != nil {
		bc.db.Close()
	}
	conn.Close()

}

func StartServer(nodeID, minerAddress string) {
	nodeAddress = fmt.Sprintf("localhost:%s", nodeID)
	miningAddress = minerAddress
	ln, err := net.Listen(protocol, nodeAddress)
	if err != nil {
		log.Panic(err)
	}

	defer ln.Close()

	InitNewWork()

	dbFile := fmt.Sprintf(dbFile, nodeID)
	var bc *Blockchain
	if dbExists(dbFile) == false {
		bc = nil
		sendVersion(fullNodes[0], bc)
		fmt.Println("I don't have a blockchain.")
		fmt.Printf("%s sends version to %s\n", nodeAddress, fullNodes[0])
	} else {
		bc = NewBlockchain(nodeID)
		height, _ := bc.GetBestHeight()
		bc.db.Close()
		fmt.Printf("I already have a blockchain with height %d.\n", height)
		if ElementInStrSlice(fullNodes, nodeAddress) == false {
			sendVersion(fullNodes[0], bc)
			fmt.Printf("%s sends version to %s\n", nodeAddress, fullNodes[0])
		}
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Panic(err)
		}

		if bc != nil {
			bc = NewBlockchain(nodeID)
		}

		// print the state of that node
		fmt.Printf("========My Address: %s=========Miner: %s=============\n", nodeAddress, miningAddress)
		fmt.Printf("Neighbor: %s\n", knownNodes)
		fmt.Printf("Full Node: %s\n", fullNodes)
		fmt.Println("=========================================================================")

		go handleConnection(conn, bc)
	}

}

func gobEncode(data interface{}) []byte {
	var buff bytes.Buffer

	enc := gob.NewEncoder(&buff)
	err := enc.Encode(data)
	if err != nil {
		log.Panic(err)
	}

	return buff.Bytes()
}

func nodeIsKnown(addr string) bool {
	for node, _ := range knownNodes {
		if node == addr {
			return true
		}
	}

	return false
}
