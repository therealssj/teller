package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"math/rand"
	"time"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/btcsuite/btcutil"
)

type updateInfo struct {
	UpdateType   string `json:"type"`
	Time         string `json:"elapsed"`
	BlockScanned int64  `json:"scanned_block"`
}

type newBTCAddress struct {
	Addresses []string `json:"btc_addresses"`
}

// Address records information about a scanned address
type Address struct {
	Addr         string `json:"address"`
	MinScanBlock int64  `json:"min_scan_block"`
	MidScanBlock int64  `json:"mid_scan_block"`
	MaxScanBlock int64  `json:"max_scan_block"`
	Txs          []Tx   `json:"txs"`
}

// Tx records information about a scanned transaction
type Tx struct {
	TxHash        string `json:"tx_hash"`
	Address       string `json:"btc_address"`
	BlockHash     string `json:"block_hash"`
	ParentHash    string `json:"parent_hash"`
	BlockHeight   int64  `json:"block_height"`
	SatoshiAmount int64  `json:"satoshi_amount"`
	BitcoinAmount string `json:"bitcoin_amount"`
}

// Deposit represents a single address's transaction
type Deposit struct {
	Addr string
	Tx   Tx
}

func scanBlock(client *rpcclient.Client, blockID int64) ([]Deposit, error) {
	blockHash, err := client.GetBlockHash(blockID)
	if err != nil {
		return nil, err
	}
	block, err := client.GetBlockVerboseTx(blockHash)
	if err != nil {
		return nil, err
	}
	parentHash := block.PreviousHash
	var deposits []Deposit
	var depTx Tx
	depTx.BlockHeight = blockID
	depTx.BlockHash = blockHash.String()
	depTx.ParentHash = parentHash
	for _, tx := range block.RawTx {
		for i, addr := range tx.Vout {
			depTx.TxHash = fmt.Sprintf("%s:%d", tx.Txid, i)
			// Because btcutil.Amount.String() adds " BTC" to the string amount, format it ourselves
			depTx.BitcoinAmount = strconv.FormatFloat(addr.Value, 'f', -int(8), 64)
			satoshis, err := btcutil.NewAmount(addr.Value)
			if err != nil {
				return nil, err
			}
			depTx.SatoshiAmount = int64(satoshis)
			for _, newAddr := range addr.ScriptPubKey.Addresses {
				depTx.Address = newAddr
				deposits = append(deposits, Deposit{
					Addr: newAddr,
					Tx:   depTx,
				})
			}
		}

	}
	return deposits, nil
}

func findTxs(addr Address, deps []Deposit) []Tx {
	var txs []Tx
	for _, dep := range deps {
		if addr.Addr == dep.Addr && !existTx(addr, dep.Tx) {
			txs = append(txs, dep.Tx)
		}
	}
	return txs
}

func existTx(addr Address, tx Tx) bool {
	for _, t := range addr.Txs {
		if t == tx {
			return true
		}
	}
	return false
}

func existAddress(newAddr Address, walletAddresses []Address) bool {
	for _, addr := range walletAddresses {
		if newAddr.Addr == addr.Addr {
			return true
		}
	}
	return false
}

func updateAddressInfo(addrs []Address, deps []Deposit, blockID int64) []Address {
	for i, addr := range addrs {
		switch {
		case addr.MaxScanBlock == 0 && blockID > 1:
			txs := findTxs(addr, deps)
			addrs[i].Txs = append(addr.Txs, txs...)
			addrs[i].MaxScanBlock = blockID
			addrs[i].MidScanBlock = blockID
		case addr.MinScanBlock < addr.MaxScanBlock && addr.MinScanBlock == (blockID-1):
			txs := findTxs(addr, deps)
			addrs[i].Txs = append(addr.Txs, txs...)
			addrs[i].MinScanBlock = blockID
			if addrs[i].MinScanBlock > addrs[i].MidScanBlock {
				addrs[i].MidScanBlock = addrs[i].MinScanBlock
			}
		case addr.MaxScanBlock > addr.MinScanBlock && addr.MaxScanBlock == (blockID-1):
			txs := findTxs(addr, deps)
			addrs[i].Txs = append(addr.Txs, txs...)
			addrs[i].MaxScanBlock = blockID
		case addr.MinScanBlock == addr.MidScanBlock && addr.MinScanBlock == addr.MaxScanBlock && addr.MaxScanBlock == (blockID-1):
			txs := findTxs(addr, deps)
			addrs[i].Txs = append(addr.Txs, txs...)
			addrs[i].MaxScanBlock = blockID
			addrs[i].MidScanBlock = blockID
			addrs[i].MinScanBlock = blockID
		}

	}
	return addrs
}

func loadWallet(file string) ([]Address, error) {
	var addrs []Address
	wallet, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	jsonParser := json.NewDecoder(wallet)
	err = jsonParser.Decode(&addrs)
	if err != nil {
		return nil, err
	}
	return addrs, wallet.Close()
}

func saveWallet(file string, addrs []Address) error {
	wallet, err := os.Open(file)
	if err != nil {
		return err
	}
	addrsJSON, err := json.MarshalIndent(addrs, "", "    ")
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(file, addrsJSON, 0644)
	if err != nil {
		return err
	}
	return wallet.Close()
}

func loadBTCFromFile(file string) (newBTCAddress, error) {
	var addrs newBTCAddress
	userFile, err := os.Open(file)
	if err != nil {
		return addrs, err
	}
	jsonParser := json.NewDecoder(userFile)
	err = jsonParser.Decode(&addrs)
	if err != nil {
		return addrs, err
	}
	return addrs, userFile.Close()
}

func addBTCAddress(addr string, file string) error {
	newAddr := Address{
		Addr:         addr,
		MinScanBlock: 0,
		MidScanBlock: 0,
		MaxScanBlock: 0,
		Txs:          []Tx{},
	}

	addrs, err := loadWallet(file)
	if err != nil {
		return err
	}

	if !existAddress(newAddr, addrs) {
		addrs = append(addrs, newAddr)
	}

	return saveWallet(file, addrs)
}

func newBTCDClient(username, pass string) (*rpcclient.Client, error) {
	//find path to btcd
	btcdHomeDir := btcutil.AppDataDir("btcd", false)
	certs, err := ioutil.ReadFile(filepath.Join(btcdHomeDir, "rpc.cert"))
	if err != nil {
		return nil, err
	}
	//config settings
	connCfg := &rpcclient.ConnConfig{
		Host:         "localhost:8334",
		Endpoint:     "ws",
		User:         username,
		Pass:         pass,
		Certificates: certs,
	}
	client, err := rpcclient.New(connCfg, nil)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func findMin(addrs []Address) int64 {
	min := addrs[0].MinScanBlock
	for _, a := range addrs {
		if a.MinScanBlock < min {
			min = a.MinScanBlock
		}
	}

	return min + 1
}

func findMax(addrs []Address) int64 {
	max := addrs[0].MaxScanBlock
	for _, a := range addrs {
		if a.MaxScanBlock > max {
			max = a.MaxScanBlock
		}
	}

	return max + 1
}

func findMid(addrs []Address) int64 {
	mid := addrs[0].MidScanBlock
	for _, a := range addrs {
		if a.MidScanBlock > mid {
			mid = a.MidScanBlock
		}
	}

	return mid + 1
}

func findShort(addrs []Address) int64 {
	dif := int64(0)
	short := addrs[0].MinScanBlock
	for _, a := range addrs {
		if a.MidScanBlock-a.MinScanBlock > 0 {
			dif = a.MidScanBlock - a.MinScanBlock
			short = a.MinScanBlock
			break
		}
	}

	for _, a := range addrs {
		if a.MidScanBlock-a.MinScanBlock < dif && a.MidScanBlock-a.MinScanBlock != 0 {
			dif = a.MidScanBlock - a.MinScanBlock
			short = a.MinScanBlock
		}
	}

	return short + 1
}

func findFar(addrs []Address) int64 {
	min := findMin(addrs)
	mid := findMid(addrs)
	dif := int64(0)
	far := min
	for _, a := range addrs {

		if a.MinScanBlock-min > dif && a.MinScanBlock < mid {
			dif = a.MinScanBlock - min
			far = a.MinScanBlock
		}
	}

	return far
}

func printUpdateInfo(updateType string, elapsed float64, scannedBlock int64) error {
	var info updateInfo
	info.UpdateType = updateType
	info.BlockScanned = scannedBlock
	info.Time = strconv.FormatFloat(elapsed, 'f', -1, 64) + "s"
	res, err := json.MarshalIndent(info, "", "		")
	if err != nil {
		return err
	}
	fmt.Println(string(res))
	return nil
}

func updateMin(addrs []Address, client *rpcclient.Client) ([]Address, error) {
	startTime := time.Now()
	min := findMin(addrs)

	deposits, err := scanBlock(client, min)
	if err != nil {
		fmt.Println("Block scanning is failed:", err)
		return nil, err
	}

	addrs = updateAddressInfo(addrs, deposits, min)
	finishTime := time.Now()
	err = printUpdateInfo("min", finishTime.Sub(startTime).Seconds(), min)
	if err != nil {
		return nil, err
	}
	return addrs, nil
}

func updateMax(addrs []Address, client *rpcclient.Client) ([]Address, error) {

	startTime := time.Now()
	max := findMax(addrs)

	deposits, err := scanBlock(client, max)
	if err != nil {
		fmt.Println("Block scanning is failed:", err)
		return nil, err
	}

	addrs = updateAddressInfo(addrs, deposits, max)
	finishTime := time.Now()
	err = printUpdateInfo("max", finishTime.Sub(startTime).Seconds(), max)
	if err != nil {
		return nil, err
	}
	return addrs, nil
}

func updateShort(addrs []Address, client *rpcclient.Client) ([]Address, error) {

	startTime := time.Now()
	short := findShort(addrs)

	deposits, err := scanBlock(client, short)
	if err != nil {
		fmt.Println("Block scanning is failed:", err)
		return nil, err
	}

	addrs = updateAddressInfo(addrs, deposits, short)
	finishTime := time.Now()
	err = printUpdateInfo("short", finishTime.Sub(startTime).Seconds(), short)
	if err != nil {
		return nil, err
	}
	return addrs, nil
}

func updateFar(addrs []Address, client *rpcclient.Client) ([]Address, error) {

	startTime := time.Now()
	far := findFar(addrs)

	deposits, err := scanBlock(client, far)
	if err != nil {
		fmt.Println("Block scanning is failed:", err)
		return nil, err
	}

	addrs = updateAddressInfo(addrs, deposits, far)
	finishTime := time.Now()
	err = printUpdateInfo("far", finishTime.Sub(startTime).Seconds(), far)
	if err != nil {
		return nil, err
	}
	return addrs, nil
}

func run() error {
	//flags
	user := flag.String("user", "myuser", "btcd username")
	pass := flag.String("pass", "SomeDecentp4ssw0rd", "btcd password")
	wallet := flag.String("wallet", "wallet.json", "path to wallet.json file")
	blockN := flag.Int64("n", 0, "start blockID")
	blockM := flag.Int64("m", 0, "finish blockID")
	add := flag.String("add", "", "add new btc addresses to wallet")
	addFile := flag.String("add_file", "", "new btc addresses from file")
	bUpdateMin := flag.Bool("update_min", false, "look for min and update 1 block forward")
	bUpdateMax := flag.Bool("update_max", false, "look for max and update 1 block forward")
	bUpdateShort := flag.Bool("update_short", false, "look for min(max-min) and update 1 block forward")
	bUpdateFar := flag.Bool("update_far", false, "look for min(mid-min) and update 1 block forward")
	randomize := flag.Bool("randomize", false, "randomly update 1 block forward by min/max/short")
	flag.Parse()

	if *add != "" {
		newBTCAddrs := strings.Split(*add, ",")
		for _, addr := range newBTCAddrs {
			if err := addBTCAddress(addr, *wallet); err != nil {
				return err
			}
		}
		fmt.Println("Addresses from command line added.")
	}

	if *addFile != "" {
		newBTC, err := loadBTCFromFile(*addFile)
		if err != nil {
			return err
		}
		for _, addr := range newBTC.Addresses {
			if err := addBTCAddress(addr, *wallet); err != nil {
				return err
			}
		}
		fmt.Println("Addresses from file added.")
	}
	//flags validation
	if *blockN < 0 || *blockM < 0 || *blockM < *blockN {
		return errors.New("Bad block range")
	}

	addrs, err := loadWallet(*wallet)
	if err != nil {
		fmt.Println("Wallet loading is failed:", err)
		return err
	}

	//create btcd instance
	client, err := newBTCDClient(*user, *pass)
	if err != nil {
		return err
	}
	defer client.Shutdown()

	for i := int(*blockN); i <= int(*blockM); i++ {
		//fmt.Println("Scannig block: ", i)
		deposits, err := scanBlock(client, int64(i))
		if err != nil {
			fmt.Println("Block scanning is failed:", err)
			return err
		}

		addrs = updateAddressInfo(addrs, deposits, int64(i))

	}

	if *bUpdateMin {
		addrs, err = updateMin(addrs, client)
		if err != nil {
			return err
		}
	}

	if *bUpdateMax {
		addrs, err = updateMax(addrs, client)
		if err != nil {
			return err
		}
	}

	if *bUpdateShort {
		addrs, err = updateShort(addrs, client)
		if err != nil {
			return err
		}
	}

	if *bUpdateFar {
		addrs, err = updateFar(addrs, client)
		if err != nil {
			return err
		}
	}

	if *randomize {
		rand.Seed(time.Now().UnixNano())
		n := rand.Intn(4)
		switch n {
		case 0:
			addrs, err = updateMin(addrs, client)
			if err != nil {
				return err
			}
		case 1:
			addrs, err = updateMax(addrs, client)
			if err != nil {
				return err
			}
		case 2:
			addrs, err = updateShort(addrs, client)
			if err != nil {
				return err
			}
		case 3:
			addrs, err = updateFar(addrs, client)
			if err != nil {
				return err
			}
		}
	}

	err = saveWallet(*wallet, addrs)
	if err != nil {
		fmt.Println("Saving wallet is failed:", err)
		return err
	}
	return nil
}

func main() {

	if err := run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

}
